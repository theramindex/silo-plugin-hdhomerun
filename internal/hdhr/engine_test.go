package hdhr

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testDevice(t *testing.T, name string, tuners int, busy *atomic.Bool) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discover.json":
			json.NewEncoder(w).Encode(map[string]any{"FriendlyName": name, "DeviceID": "EMULATED-SAME-ID", "TunerCount": tuners})
		case "/lineup.json":
			json.NewEncoder(w).Encode([]map[string]any{{"GuideNumber": "5.1", "GuideName": "Local News", "URL": server.URL + "/stream"}})
		case "/stream":
			if busy != nil && busy.Load() {
				w.Header().Set("X-HDHomeRun-Error", "805 All Tuners In Use")
				w.WriteHeader(503)
				return
			}
			w.Header().Set("Content-Type", "video/mp2t")
			fmt.Fprint(w, name)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	return server
}
func TestMultipleEndpointsHaveStableIdentitiesAndExplicitFailover(t *testing.T) {
	var busy atomic.Bool
	busy.Store(true)
	first := testDevice(t, "first", 1, &busy)
	second := testDevice(t, "second", 1, nil)
	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	cfg := Config{Endpoints: []Endpoint{{ID: "first", URL: first.URL}, {ID: "second", URL: second.URL}}}
	if err = e.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err = e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	channels := e.Snapshot().Channels
	if len(channels) != 2 || channels[0].ID == channels[1].ID {
		t.Fatalf("separate sources must not collide despite equal device IDs/numbers: %+v", channels)
	}
	original := channels[0].ID
	cfg.Endpoints[0].Name = "Renamed"
	if err = e.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	_ = e.Refresh(context.Background())
	if e.Snapshot().Channels[0].ID != original {
		t.Fatal("renaming an endpoint changed channel identity")
	}
	for i := range cfg.Endpoints {
		cfg.Endpoints[i].Channels = map[string]ChannelMapping{"5.1": {StationID: "local-news"}}
	}
	if err = e.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	_ = e.Refresh(context.Background())
	channels = e.Snapshot().Channels
	if len(channels) != 1 {
		t.Fatalf("explicit station mapping must merge sources: %+v", channels)
	}
	stream, err := e.Resolve(channels[0].ID, "play-one", "user:1", "ts")
	if err != nil {
		t.Fatal(err)
	}
	same, err := e.Resolve(channels[0].ID, "play-one", "user:1", "ts")
	if err != nil || same.URL != stream.URL {
		t.Fatal("repeated session resolution must be idempotent")
	}
	if _, err = e.Resolve(channels[0].ID, "play-one", "user:2", "ts"); err == nil {
		t.Fatal("a second user must not adopt a reservation")
	}
	if e.Stop("play-one", "user:2") == nil {
		t.Fatal("a second user must not stop another user's session")
	}
	r, err := http.Get(stream.URL)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || string(data) != "second" {
		t.Fatalf("busy first endpoint must fail over before returning data: status=%d body=%q", r.StatusCode, data)
	}
	if err = e.Stop("play-one", "user:1"); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentReservationsRespectPerEndpointTunerLimits(t *testing.T) {
	server := testDevice(t, "one", 2, nil)
	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	_ = e.Configure(Config{Endpoints: []Endpoint{{ID: "one", URL: server.URL}}})
	_ = e.Refresh(context.Background())
	id := e.Snapshot().Channels[0].ID
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := e.Resolve(id, fmt.Sprintf("session-%d", i), "user:1", "ts"); err == nil {
				accepted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 2 {
		t.Fatalf("got %d reservations for two tuners", accepted.Load())
	}
	if got := e.Snapshot().Endpoints[0].InUse; got != 2 {
		t.Fatalf("reported %d tuners in use", got)
	}
}
func TestDeviceCannotAdvertiseAnUnconfiguredHost(t *testing.T) {
	var hit atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Store(true) }))
	defer other.Close()
	// Same IP on a different port is the same configured device. Use a distinct
	// hostname so this checks advertised-host admission, not a failed TCP dial.
	unapproved := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"LineupURL": unapproved})
	}))
	defer source.Close()
	client, err := NewDeviceClient(context.Background(), Endpoint{URL: source.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	d, err := client.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Lineup(context.Background(), d); err == nil || hit.Load() {
		t.Fatal("an advertised host escaped the configured endpoint")
	}
}

func TestLineupsShareServerCapacityWithoutMergingStations(t *testing.T) {
	var busy atomic.Bool
	first := testDevice(t, "Profile one", 2, &busy)
	defer first.Close()
	second := testDevice(t, "Profile two", 2, &busy)
	defer second.Close()
	engine, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	config := Config{Endpoints: []Endpoint{{ID: "one", URL: first.URL, CapacityGroup: "server", SharedTunerLimit: 1}, {ID: "two", URL: second.URL, CapacityGroup: "server", SharedTunerLimit: 1}}}
	if err = engine.Configure(config); err != nil {
		t.Fatal(err)
	}
	if err = engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	channels := engine.Snapshot().Channels
	if len(channels) != 2 {
		t.Fatalf("independent station identities changed: %d", len(channels))
	}
	if _, err = engine.Resolve(channels[0].ID, "first", "viewer", "any"); err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Resolve(channels[1].ID, "second", "viewer", "any"); err != ErrConnectionLimit {
		t.Fatalf("profile bypassed parent server limit: %v", err)
	}
	if err = engine.Stop("first", "viewer"); err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Resolve(channels[1].ID, "second", "viewer", "any"); err != nil {
		t.Fatal("released capacity was not reusable", err)
	}
}

func TestLargeGuideSurvivesRestartDuringUpstreamOutage(t *testing.T) {
	const count = 8500
	var offline atomic.Bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			w.WriteHeader(503)
			return
		}
		switch r.URL.Path {
		case "/discover.json":
			json.NewEncoder(w).Encode(map[string]any{"DeviceID": "cache-test", "TunerCount": 2})
		case "/lineup.json":
			json.NewEncoder(w).Encode([]map[string]any{{"GuideNumber": "5.1", "GuideName": "News", "URL": server.URL + "/live.ts"}})
		case "/guide.xml":
			b := bufio.NewWriterSize(w, 64<<10)
			defer b.Flush()
			io.WriteString(b, `<tv><channel id="5.1"><display-name>News</display-name></channel>`)
			description := strings.Repeat("x", 8192)
			now := time.Now().UTC().Truncate(time.Minute)
			for i := 0; i < count; i++ {
				from := now.Add(time.Duration(i) * time.Minute)
				fmt.Fprintf(b, `<programme channel="5.1" start="%s" stop="%s"><title>Programme %d</title><desc>%s</desc></programme>`, from.Format("20060102150405 -0700"), from.Add(time.Minute).Format("20060102150405 -0700"), i, description)
			}
			io.WriteString(b, "</tv>")
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	cfg := Config{Endpoints: []Endpoint{{ID: "one", URL: server.URL, GuideMode: "xmltv", GuideURL: server.URL + "/guide.xml"}}}
	first, err := NewEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err = first.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := first.Snapshot().Endpoints[0].ProgramCount; got != count {
		t.Fatalf("fresh guide: got %d, want %d", got, count)
	}
	first.Close()
	offline.Store(true)
	restored, err := NewEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err = restored.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	_ = restored.Refresh(context.Background())
	snap := restored.Snapshot()
	actual, _, readErr := restored.guideView.Coverage(context.Background(), 0)
	if readErr != nil || actual != count {
		t.Fatalf("stored guide rows: %d %v", actual, readErr)
	}
	if snap.Endpoints[0].ProgramCount != count || len(snap.Channels) != 1 {
		t.Fatalf("cached guide lost after restart: %d programmes, %d channels", snap.Endpoints[0].ProgramCount, len(snap.Channels))
	}
}

package hdhr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHLSRewritesEveryResourceAndStopCancelsProducer(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	var device *httptest.Server
	device = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discover.json":
			json.NewEncoder(w).Encode(map[string]any{"TunerCount": 1})
		case "/lineup.json":
			json.NewEncoder(w).Encode([]LineupChannel{{Number: "1", Name: "One", URL: device.URL + "/index.m3u8"}})
		case "/index.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-KEY:METHOD=AES-128,URI=\"key?secret=value\"\n#EXTINF:4,\nsegment.ts?credential=private\n")
		case "/key":
			fmt.Fprint(w, "1234567890123456")
		case "/segment.ts":
			close(started)
			w.Header().Set("Content-Type", "video/mp2t")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(stopped)
		default:
			w.WriteHeader(404)
		}
	}))
	defer device.Close()
	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	_ = e.Configure(Config{Endpoints: []Endpoint{{ID: "one", URL: device.URL}}})
	_ = e.Refresh(context.Background())
	stream, err := e.Resolve(e.Snapshot().Channels[0].ID, "session", "viewer", "hls")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(stream.URL)
	parts := strings.Split(u.Path, "/")
	token := parts[2]
	relay, err := e.RelayURL(token, "viewer", "index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	r, err := http.Get(relay)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(r.Body)
	r.Body.Close()
	body := string(data)
	if r.StatusCode != 200 || strings.Contains(body, device.URL) || strings.Contains(body, "private") || strings.Contains(body, "value") || !strings.Contains(body, `URI="asset/`) {
		t.Fatalf("playlist leaked source or failed rewriting: %s", body)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	segment := lines[len(lines)-1]
	if !strings.HasPrefix(segment, "asset/") {
		t.Fatalf("relative segment route: %s", segment)
	}
	target, err := e.RelayURL(token, "viewer", segment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.RelayURL(token, "other", segment); err != ErrSessionOwner {
		t.Fatalf("another viewer adopted a stream: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r, err := http.Get(target)
		if err == nil {
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("segment producer did not start")
	}
	if err = e.Stop("session", "viewer"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stopping HLS left upstream tuner request alive")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("segment handler did not stop")
	}
	if _, err = e.RelayURL(token, "viewer", segment); err != ErrSessionNotFound {
		t.Fatal("stopped resource remained authorized")
	}
}
func TestHeadDoesNotTuneAndExpiryFreesReservation(t *testing.T) {
	device := testDevice(t, "test", 1, nil)
	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	_ = e.Configure(Config{Endpoints: []Endpoint{{ID: "one", URL: device.URL}}})
	_ = e.Refresh(context.Background())
	id := e.Snapshot().Channels[0].ID
	stream, err := e.Resolve(id, "first", "viewer", "ts")
	if err != nil {
		t.Fatal(err)
	}
	r, err := http.Head(stream.URL)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 || e.Snapshot().Endpoints[0].InUse != 1 {
		t.Fatal("HEAD consumed or released the reserved session")
	}
	e.mu.Lock()
	e.leases["first"].Expires = time.Now().Add(-time.Second)
	e.mu.Unlock()
	if _, err = e.Resolve(id, "next", "viewer", "ts"); err != nil {
		t.Fatalf("expired reservation still occupied tuner: %v", err)
	}
	r, err = http.Get(stream.URL)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 404 {
		t.Fatalf("expired stream still active: %d", r.StatusCode)
	}
}

package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/theramindex/silo-plugin-hdhomerun/internal/hdhr"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestLiveTVContractAndViewerMediaAuthorization(t *testing.T) {
	var device *httptest.Server
	device = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discover.json":
			fmt.Fprint(w, `{"TunerCount":2,"DeviceAuth":"private-device-auth"}`)
		case "/lineup.json":
			json.NewEncoder(w).Encode([]map[string]any{{"GuideNumber": "7", "GuideName": "Local", "URL": device.URL + "/stream?secret=private"}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer device.Close()
	e, err := hdhr.NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Configure(hdhr.Config{Endpoints: []hdhr.Endpoint{{ID: "local", URL: device.URL}}}); err != nil {
		t.Fatal(err)
	}
	if err = e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := &Server{Engine: e}
	call := func(path, method, user, role string, q map[string]any, body string) *pluginv1.HandleHTTPResponse {
		t.Helper()
		query, _ := structpb.NewStruct(q)
		h := map[string]string{"X-Silo-User-Id": user, "X-Silo-User-Role": role, "X-Silo-Profile-Id": "profile"}
		r, err := s.Handle(context.Background(), &pluginv1.HandleHTTPRequest{Path: path, Method: method, Headers: h, Query: query, Body: []byte(body)})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	// Silo loads the lineup in process before a viewer is present; public HTTP
	// access is independently restricted by the manifest and host proxy.
	channels := call("/silo-compat/livetv/v1/channels", "GET", "", "", nil, "")
	if channels.StatusCode != 200 {
		t.Fatalf("anonymous in-process lineup: %d", channels.StatusCode)
	}
	for _, p := range []string{"/silo-compat/livetv/v1/channels", "/hdhomerun/api/state"} {
		r := call(p, "GET", "1", "user", nil, "")
		if strings.Contains(string(r.Body), "secret") || strings.Contains(string(r.Body), "private-device-auth") || strings.Contains(string(r.Body), device.URL) {
			t.Fatalf("public metadata leaked a source address: %s", r.Body)
		}
	}
	if r := call("/hdhomerun/admin/api/config", "GET", "1", "user", nil, ""); r.StatusCode != 403 {
		t.Fatal("ordinary viewer could read endpoint settings")
	}
	if r := call("/hdhomerun/api/state", "GET", "", "", nil, ""); r.StatusCode != 401 {
		t.Fatal("web API accepted missing identity")
	}
	id := e.Snapshot().Channels[0].ID
	request := fmt.Sprintf(`{"channelId":%q,"sessionId":"watch","format":"ts"}`, id)
	played := call("/hdhomerun/api/play", "POST", "1", "user", nil, request)
	if played.StatusCode != 200 {
		t.Fatalf("play failed: %s", played.Body)
	}
	var play struct{ Path string }
	json.Unmarshal(played.Body, &play)
	if !strings.HasPrefix(play.Path, "/hdhomerun/media/") || strings.Contains(string(played.Body), "127.0.0.1") {
		t.Fatalf("browser got an upstream address: %s", played.Body)
	}
	if r := call(play.Path, "GET", "2", "user", nil, ""); r.StatusCode != 403 {
		t.Fatal("another account could relay media")
	}
	foreign, err := s.Handle(context.Background(), &pluginv1.HandleHTTPRequest{Path: play.Path, Method: "GET", Headers: map[string]string{"X-Silo-User-Id": "1", "X-Silo-Profile-Id": "other-profile", "X-Silo-User-Role": "user"}})
	if err != nil || foreign.StatusCode != 403 {
		t.Fatal("another household profile could relay media")
	}
	if r := call(play.Path, "GET", "1", "user", nil, ""); r.StatusCode != 200 || r.Headers["X-Silo-Relay-URL"] == "" {
		t.Fatal("owner did not receive host relay instruction")
	}
	if r := call("/hdhomerun/api/session/stop", "POST", "2", "user", map[string]any{"session_id": "watch"}, ""); r.StatusCode != 403 {
		t.Fatal("another account could stop playback")
	}
	if r := call("/hdhomerun/api/session/stop", "POST", "1", "user", map[string]any{"session_id": "watch"}, ""); r.StatusCode != 200 {
		t.Fatal("owner could not stop playback")
	}
	if r := call(play.Path, "GET", "1", "user", nil, ""); r.StatusCode != 404 {
		t.Fatal("stopped browser route remained active")
	}
	for _, q := range []map[string]any{{}, {"start": "0", "end": "1", "class": "invalid"}, {"start": "2", "end": "1"}} {
		if r := call("/silo-compat/livetv/v1/programs", "GET", "1", "user", q, ""); r.StatusCode != 400 {
			t.Fatal("invalid guide query accepted")
		}
	}
	now := time.Now().Unix()
	q := map[string]any{"start": fmt.Sprint(now), "end": fmt.Sprint(now + 3600)}
	if r := call("/silo-compat/livetv/v1/programs", "GET", "1", "user", q, ""); r.StatusCode != 200 || string(r.Body) != `{"programs":[]}` {
		t.Fatalf("no-guide fallback: %s", r.Body)
	}
}

func TestFreshInstallConfigSupportsSetup(t *testing.T) {
	engine, err := hdhr.NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	server := &Server{Engine: engine}
	// A fresh Silo installation has not supplied a connection configuration yet.
	response, err := server.Handle(context.Background(), &pluginv1.HandleHTTPRequest{
		Method: "GET", Path: "/hdhomerun/admin/api/config",
		Headers: map[string]string{"X-Silo-User-Id": "1", "X-Silo-User-Role": "admin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("setup config: status=%d body=%s", response.StatusCode, response.Body)
	}
	var config map[string]any
	if err := json.Unmarshal(response.Body, &config); err != nil {
		t.Fatal(err)
	}
	endpoints, ok := config["endpoints"].([]any)
	if !ok || len(endpoints) != 0 {
		t.Fatalf("fresh setup requires an empty endpoint array, got %s", response.Body)
	}
}

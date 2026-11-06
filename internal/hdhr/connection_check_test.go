package hdhr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func checkTestDevice(t *testing.T, channels string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/discover.json":
			_ = json.NewEncoder(w).Encode(map[string]any{"DeviceID": "check", "LineupURL": "lineup.json"})
		case "/lineup.json":
			_, _ = w.Write([]byte(channels))
		default:
			http.Error(w, "sensitive-upstream-error", http.StatusBadGateway)
		}
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func offlineAddress(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.NotFoundHandler())
	address := server.URL
	server.Close()
	return address
}

// The Devices editor stages only the endpoint being edited, so an offline
// saved tuner is never contacted and cannot fail the check.
func TestConnectionCheckProbesOnlyTheStagedEndpoint(t *testing.T) {
	draft, draftRequests := checkTestDevice(t, `[{"GuideNumber":"1","GuideName":"One","URL":"/stream"}]`)
	saved, savedRequests := checkTestDevice(t, `[]`)
	engine, err := NewEngine("")
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err := engine.Configure(Config{Endpoints: []Endpoint{{ID: "saved", Name: "Saved tuner", URL: saved.URL}}}); err != nil {
		t.Fatal(err)
	}
	if err := CheckConfiguration(t.Context(), Config{Endpoints: []Endpoint{{ID: "draft", URL: draft.URL}}}); err != nil {
		t.Fatalf("draft check failed: %v", err)
	}
	if draftRequests.Load() != 2 {
		t.Fatalf("draft requests = %d, want discovery and lineup", draftRequests.Load())
	}
	if savedRequests.Load() != 0 {
		t.Fatal("check contacted a saved endpoint that was not staged")
	}
}

func TestConnectionCheckErrorCopy(t *testing.T) {
	empty, _ := checkTestDevice(t, `[]`)
	working, _ := checkTestDevice(t, `[{"GuideNumber":"1","GuideName":"One","URL":"/stream"}]`)
	offline := offlineAddress(t)
	off := false
	for _, tc := range []struct {
		name      string
		endpoints []Endpoint
		want      error
		message   string
	}{
		{name: "unreachable device", endpoints: []Endpoint{{ID: "draft", Name: "Den tuner", URL: offline}}, want: errCheckDiscovery},
		{name: "empty lineup", endpoints: []Endpoint{{ID: "draft", Name: "Den tuner", URL: empty.URL}}, want: errCheckChannels},
		{name: "disabled draft", endpoints: []Endpoint{{ID: "draft", URL: working.URL, Enabled: &off}}, message: "Enable the endpoint before checking its connection."},
		{name: "several staged endpoints keep their position", endpoints: []Endpoint{{ID: "one", URL: working.URL}, {ID: "two", Name: "Den tuner", URL: offline}}, message: "Endpoint 2: " + errCheckDiscovery.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckConfiguration(t.Context(), Config{Endpoints: tc.endpoints})
			if err == nil {
				t.Fatal("check should fail")
			}
			message := err.Error()
			if tc.want != nil && message != tc.want.Error() {
				t.Fatalf("message = %q, want %q", message, tc.want)
			}
			if tc.message != "" && message != tc.message {
				t.Fatalf("message = %q, want %q", message, tc.message)
			}
			if len(tc.endpoints) == 1 && strings.HasPrefix(message, "Endpoint ") {
				t.Fatalf("single draft check kept a positional prefix: %q", message)
			}
			for _, secret := range []string{"Den tuner", offline, empty.URL, working.URL, "127.0.0.1", "sensitive-"} {
				if strings.Contains(message, secret) {
					t.Fatalf("message %q echoes %q", message, secret)
				}
			}
			if first := message[0]; first < 'A' || first > 'Z' || !strings.HasSuffix(message, ".") {
				t.Fatalf("message %q is not a complete sentence", message)
			}
		})
	}
}

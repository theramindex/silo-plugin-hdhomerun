package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/theramindex/silo-plugin-hdhomerun/internal/hdhr"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestConfigurationCheckProbesDraftWithoutChangingDevices(t *testing.T) {
	for _, tc := range []struct {
		name            string
		empty, external bool
		ok              bool
	}{
		{name: "emulator path prefix", ok: true},
		{name: "empty lineup", empty: true},
		{name: "untrusted lineup host", external: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var discovery, lineup, other atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/emulator/discover.json":
					discovery.Add(1)
					url := "lineup.json"
					if tc.external {
						url = "http://untrusted.example.invalid/secret-lineup"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"DeviceID": "test", "LineupURL": url})
				case "/emulator/lineup.json":
					lineup.Add(1)
					if tc.empty {
						_, _ = w.Write([]byte(`[]`))
						return
					}
					_, _ = w.Write([]byte(`[{"GuideNumber":"1","GuideName":"Test","URL":"/stream"}]`))
				default:
					other.Add(1)
					http.Error(w, "sensitive-upstream-error", http.StatusBadGateway)
				}
			}))
			defer upstream.Close()
			engine, err := hdhr.NewEngine("")
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			saved := engine.Config()
			server := &runtimeServer{engine: engine}
			value, err := structpb.NewStruct(map[string]any{"endpoints": []any{map[string]any{
				"id": "draft", "url": upstream.URL + "/emulator", "guide_url": upstream.URL + "/guide?key=sensitive-guide-key",
			}}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := server.TestConfiguration(t.Context(), &pluginv1.TestConfigurationRequest{Config: []*pluginv1.ConfigEntry{{Key: "connection", Value: value}}})
			if err != nil || result.GetOk() != tc.ok {
				t.Fatalf("check = %v, %v", result, err)
			}
			if discovery.Load() != 1 || other.Load() != 0 || !tc.external && lineup.Load() != 1 {
				t.Fatal("check did not restrict requests to discovery and the trusted lineup")
			}
			if !reflect.DeepEqual(engine.Config(), saved) || len(engine.Snapshot().Channels) != 0 {
				t.Fatal("check installed the draft devices or lineup")
			}
			if strings.Contains(result.GetMessage(), "sensitive-") || strings.Contains(result.GetMessage(), upstream.URL) {
				t.Fatal("check exposed upstream credentials or response content")
			}
		})
	}
}

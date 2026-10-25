package hdhr

import "testing"

func TestConfigRejectsAmbiguousOrUnsafeEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"duplicate identity", Config{Endpoints: []Endpoint{{ID: "home", URL: "http://tuner.local"}, {ID: "home", URL: "http://other.local"}}}},
		{"credentials in device address", Config{Endpoints: []Endpoint{{ID: "home", URL: "http://user:pass@tuner.local"}}}},
		{"non HTTP device", Config{Endpoints: []Endpoint{{ID: "home", URL: "file:///etc/passwd"}}}},
		{"unbounded tuner limit", Config{Endpoints: []Endpoint{{ID: "home", URL: "http://tuner.local", TunerLimit: 10000}}}},
		{"unknown guide mode", Config{Endpoints: []Endpoint{{ID: "home", URL: "http://tuner.local", GuideMode: "scrape"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.cfg.Normalize(); err == nil {
				t.Fatal("configuration should be rejected")
			}
		})
	}
}

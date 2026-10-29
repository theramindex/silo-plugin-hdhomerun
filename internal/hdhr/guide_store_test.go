package hdhr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSQLiteGuideKeepsEndpointIdentityPriorityAndLastGoodData(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Hour)
	var emptyFirst atomic.Bool
	var secondTitle atomic.Value
	secondTitle.Store("Second source")
	device := func(first bool) *httptest.Server {
		var server *httptest.Server
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/discover.json":
				_ = json.NewEncoder(w).Encode(map[string]any{"TunerCount": 2})
			case "/lineup.json":
				_ = json.NewEncoder(w).Encode([]LineupChannel{{Number: "5.1", Name: "News", URL: server.URL + "/live.ts"}})
			case "/guide.xml":
				_, _ = fmt.Fprint(w, `<tv><channel id="news"><display-name>News</display-name></channel>`)
				if !first || !emptyFirst.Load() {
					title := "First source"
					if !first {
						title = secondTitle.Load().(string)
					}
					_, _ = fmt.Fprintf(w, `<programme channel="news" start="%s" stop="%s"><title>%s</title></programme>`, now.Format("20060102150405 -0700"), now.Add(time.Hour).Format("20060102150405 -0700"), title)
				}
				_, _ = fmt.Fprint(w, `</tv>`)
			default:
				w.WriteHeader(404)
			}
		}))
		return server
	}
	first, second := device(true), device(false)
	defer first.Close()
	defer second.Close()
	engine, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	cfg := Config{Endpoints: []Endpoint{
		{ID: "one", URL: first.URL, Priority: 0, GuideMode: "xmltv", GuideURL: first.URL + "/guide.xml"},
		{ID: "two", URL: second.URL, Priority: 10, GuideMode: "xmltv", GuideURL: second.URL + "/guide.xml"},
	}}
	if err = engine.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err = engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	programs, err := engine.Programs(context.Background(), nil, now.Unix(), now.Add(time.Hour).Unix())
	if err != nil || len(programs) != 2 || programs[0].ChannelID == programs[1].ChannelID {
		t.Fatalf("endpoints sharing an XMLTV id were merged: %+v, %v", programs, err)
	}
	revision := engine.guideView.Revision
	if err = engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if engine.guideView.Revision != revision {
		t.Fatal("lineup-only refresh rewrote programme rows")
	}
	emptyFirst.Store(true)
	secondTitle.Store("Updated second source")
	engine.mu.Lock()
	for id, state := range engine.devices {
		state.NextGuideUnix = 0
		engine.devices[id] = state
	}
	engine.mu.Unlock()
	if err = engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	programs, err = engine.Programs(context.Background(), nil, now.Unix(), now.Add(time.Hour).Unix())
	if err != nil || len(programs) != 2 || programs[0].Title != "First source" || programs[1].Title != "Updated second source" {
		t.Fatalf("partial guide refresh lost last-good data: %+v, %v", programs, err)
	}
	if engine.Snapshot().Endpoints[0].GuideError == "" {
		t.Fatal("empty upstream guide did not report its retained-cache warning")
	}
	// Only an explicit station mapping combines the endpoints. The first
	// endpoint's guide must still win when the SQL result order differs.
	emptyFirst.Store(false)
	for i := range cfg.Endpoints {
		cfg.Endpoints[i].Channels = map[string]ChannelMapping{"5.1": {StationID: "news"}}
	}
	if err = engine.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err = engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	programs, err = engine.Programs(context.Background(), []string{"station:news"}, now.Unix(), now.Add(time.Hour).Unix())
	if err != nil || len(programs) != 1 || programs[0].Title != "First source" || programs[0].ChannelID != "station:news" {
		t.Fatalf("explicit station priority changed: %+v, %v", programs, err)
	}
}

package hdhr

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"github.com/theramindex/silo-plugins/pkg/epg"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGuideRefreshRetainsPrivateSnapshotAcrossOutageAndRestart(t *testing.T) {
	var offline atomic.Bool
	now := time.Now().UTC().Truncate(time.Hour)
	xml := fmt.Sprintf(`<tv><channel id="news"><display-name>Local News</display-name></channel><programme channel="news" start="%s" stop="%s"><title>Evening bulletin</title><category>News</category><live/></programme></tv>`, now.Format("20060102150405 -0700"), now.Add(time.Hour).Format("20060102150405 -0700"))
	var device *httptest.Server
	device = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			w.WriteHeader(503)
			return
		}
		switch r.URL.Path {
		case "/discover.json":
			json.NewEncoder(w).Encode(map[string]any{"DeviceAuth": "must-not-persist", "TunerCount": 2})
		case "/lineup.json":
			json.NewEncoder(w).Encode([]LineupChannel{{Number: "5.1", Name: "Local News", URL: device.URL + "/stream"}})
		case "/guide.xml.gz":
			w.Header().Set("Content-Type", "application/gzip")
			z := gzip.NewWriter(w)
			fmt.Fprint(z, xml)
			z.Close()
		default:
			w.WriteHeader(404)
		}
	}))
	defer device.Close()
	dir := t.TempDir()
	cfg := Config{Endpoints: []Endpoint{{ID: "local", URL: device.URL, GuideMode: "xmltv", GuideURL: device.URL + "/guide.xml.gz"}}}
	e, err := NewEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err = e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap := e.Snapshot()
	programs, err := e.Programs(context.Background(), nil, math.MinInt64, math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Channels) != 1 || len(programs) != 1 || programs[0].ChannelID != snap.Channels[0].ID || !programs[0].IsNews || !programs[0].IsLive {
		t.Fatalf("guide was not joined to lineup: %+v", snap)
	}
	first := programs[0].ID
	metadata, view, found, err := e.guideStore.Load(context.Background())
	if err != nil || !found {
		t.Fatalf("persisted SQLite state: %v", err)
	}
	records, err := view.Query(context.Background(), epg.Query{Start: math.MinInt64, End: math.MaxInt64})
	if err != nil {
		t.Fatal(err)
	}
	var legacy diskState
	if err := json.Unmarshal(metadata, &legacy); err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		state := legacy.Devices[record.EndpointID]
		state.ProgramCount = 0 // Old JSON did not have a separate programme count.
		state.Guide.Programs = append(state.Guide.Programs, record.Program)
		legacy.Devices[record.EndpointID] = state
	}
	e.Close()
	data, err := os.ReadFile(filepath.Join(dir, "guide.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "must-not-persist") {
		t.Fatal("device authorization escaped into disk cache")
	}
	st, _ := os.Stat(filepath.Join(dir, "guide.sqlite"))
	if st.Mode().Perm() != 0600 {
		t.Fatalf("private cache permissions: %v", st.Mode())
	}
	// Exercise one-time import from a pre-SQLite installation, keeping its JSON.
	data, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	offline.Store(true)
	for _, name := range []string{"catalog.json", "catalog.json.gz"} {
		t.Run(name, func(t *testing.T) {
			legacyDir := t.TempDir()
			file, err := os.OpenFile(filepath.Join(legacyDir, name), os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(name, ".gz") {
				compressed := gzip.NewWriter(file)
				if _, err := compressed.Write(data); err != nil {
					t.Fatal(err)
				}
				if err := compressed.Close(); err != nil {
					t.Fatal(err)
				}
			} else if _, err := file.Write(data); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			restored, err := NewEngine(legacyDir)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			if err = restored.Configure(cfg); err != nil {
				t.Fatal(err)
			}
			_ = restored.Refresh(context.Background())
			snap := restored.Snapshot()
			programs, err := restored.Programs(context.Background(), nil, math.MinInt64, math.MaxInt64)
			if err != nil || len(snap.Channels) != 1 || len(programs) != 1 || programs[0].ID != first || snap.Endpoints[0].Error == "" {
				t.Fatalf("offline legacy import: %d channels, %v programmes, %v", len(snap.Channels), programs, err)
			}
			if len(restored.devices["local"].Guide.Programs) != 0 {
				t.Fatal("import retained the complete guide in memory")
			}
			if _, err = os.Stat(filepath.Join(legacyDir, name)); err != nil {
				t.Fatal("legacy rollback copy removed", err)
			}
		})
	}

}
func TestXMLTVRejectsBrokenDocumentsAndAmbiguousMatches(t *testing.T) {
	for _, raw := range []string{`<html></html>`, `<tv><channel id="a">`, `<tv></tv><tv></tv>`} {
		if _, err := ParseGuide(strings.NewReader(raw), time.Now()); err == nil {
			t.Fatalf("accepted broken XMLTV: %s", raw)
		}
	}
	g, err := ParseGuide(strings.NewReader(`<tv><channel id="a"><display-name>Shared</display-name></channel><channel id="b"><display-name>Shared</display-name></channel></tv>`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if g.MatchChannel(LineupChannel{Name: "Shared", Number: "7"}) != "" {
		t.Fatal("ambiguous names must not assign an arbitrary guide")
	}
	if got := g.MatchChannel(LineupChannel{Name: "Shared", Number: "7", GuideID: "b"}); got != "b" {
		t.Fatalf("explicit guide ID did not resolve ambiguity: %s", got)
	}
}

func TestGuideFillerDoesNotPopulateClassifiedRows(t *testing.T) {
	now := time.Now().UTC()
	raw := fmt.Sprintf(`<tv><programme channel="a" start="%s" stop="%s"><title>Paid Programming</title><category>Sports</category><live/></programme></tv>`, now.Format("20060102150405 -0700"), now.Add(time.Hour).Format("20060102150405 -0700"))
	g, err := ParseGuide(strings.NewReader(raw), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Programs) != 1 {
		t.Fatal("filler should remain in the grid")
	}
	p := g.Programs[0]
	if p.IsSports || p.IsMovie || p.IsNews || p.IsKids || p.IsSeries || p.IsLive {
		t.Fatal("filler polluted classified or live rows")
	}
}

func TestLargeGuideRetainsOnlyMappedLineup(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Hour)
	var xml strings.Builder
	xml.WriteString(`<tv><channel id="wanted"><display-name>News</display-name></channel><channel id="other"><display-name>Other</display-name></channel>`)
	start, end := now.Format("20060102150405 -0700"), now.Add(time.Hour).Format("20060102150405 -0700")
	for i := 0; i < 100005; i++ {
		xml.WriteString(`<programme channel="other" start="` + start + `" stop="` + end + `"><title>Unused</title></programme>`)
	}
	xml.WriteString(`<programme channel="wanted" start="` + start + `" stop="` + end + `"><title>News now</title></programme></tv>`)
	guide, err := parseGuide(strings.NewReader(xml.String()), now, []LineupChannel{{Number: "7", Name: "Local news"}}, map[string]ChannelMapping{"7": {GuideID: "wanted"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(guide.Programs) != 1 || guide.Programs[0].Title != "News now" {
		t.Fatalf("retained irrelevant programmes: %d", len(guide.Programs))
	}
	// Full lineups may legitimately exceed the old 100k limit, still bounded by
	// decoded bytes and the one-million retained-programme budget.
	all, err := ParseGuide(strings.NewReader(xml.String()), now)
	if err != nil || len(all.Programs) != 100006 {
		t.Fatalf("large complete feed: programmes=%d err=%v", len(all.Programs), err)
	}
}

func TestGuideFilteringSupportsInterleavedChannelsAndAmbiguousNames(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Hour)
	start, end := now.Format("20060102150405 -0700"), now.Add(time.Hour).Format("20060102150405 -0700")
	program := func(id string) string {
		return `<programme channel="` + id + `" start="` + start + `" stop="` + end + `"><title>` + id + `</title></programme>`
	}
	xml := `<tv><channel id="a"><display-name>News</display-name></channel>` + program("a") + `<channel id="b"><display-name>Culture</display-name></channel>` + program("b") + `<channel id="c"><display-name>News</display-name></channel>` + program("c") + `</tv>`
	guide, err := parseGuide(strings.NewReader(xml), now, []LineupChannel{{Number: "7", Name: "News"}, {Number: "8", Name: "Culture"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(guide.Programs) != 1 || guide.Programs[0].ChannelID != "b" {
		t.Fatalf("interleaved or ambiguous match: %+v", guide.Programs)
	}
}

package hdhr

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	if len(snap.Channels) != 1 || len(snap.Programs) != 1 || snap.Programs[0].ChannelID != snap.Channels[0].ID || !snap.Programs[0].IsNews || !snap.Programs[0].IsLive {
		t.Fatalf("guide was not joined to lineup: %+v", snap)
	}
	first := snap.Programs[0].ID
	e.Close()
	file, err := os.Open(filepath.Join(dir, "catalog.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(compressed)
	compressed.Close()
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "must-not-persist") {
		t.Fatal("device authorization escaped into disk cache")
	}
	st, _ := os.Stat(filepath.Join(dir, "catalog.json.gz"))
	if st.Mode().Perm() != 0600 {
		t.Fatalf("private cache permissions: %v", st.Mode())
	}
	// Existing installations still have plain JSON; exercise that migration.
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "catalog.json.gz")); err != nil {
		t.Fatal(err)
	}

	offline.Store(true)
	e, err = NewEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	_ = e.Refresh(context.Background())
	snap = e.Snapshot()
	if len(snap.Channels) != 1 || len(snap.Programs) != 1 || snap.Programs[0].ID != first || snap.Endpoints[0].Error == "" {
		t.Fatalf("offline restart lost catalog or hid health failure: %+v", snap)
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

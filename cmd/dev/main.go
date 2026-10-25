// Development-only local host and two HDHomeRun emulators. Never deploy this
// harness: it supplies a fixed administrator identity and in-memory settings.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/theramindex/silo-plugin-hdhomerun/internal/hdhr"
	pluginimpl "github.com/theramindex/silo-plugin-hdhomerun/internal/plugin"
	"google.golang.org/protobuf/types/known/structpb"
)

func main() {
	address := flag.String("listen", "127.0.0.1:18770", "loopback UI address")
	media := flag.String("media-dir", "", "optional ffmpeg-generated HLS fixture directory")
	flag.Parse()
	host, _, err := net.SplitHostPort(*address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("development server requires a loopback listener")
	}
	devices := []*httptest.Server{}
	for i := 0; i < 2; i++ {
		devices = append(devices, emulator(i, *media))
	}
	defer func() {
		for _, d := range devices {
			d.Close()
		}
	}()
	dir, err := os.MkdirTemp("", "hdhr-dev-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	engine, err := hdhr.NewEngine(dir)
	if err != nil {
		log.Fatal(err)
	}
	defer engine.Close()
	cfg := hdhr.Config{Endpoints: []hdhr.Endpoint{{ID: "antenna", Name: "Antenna · upstairs", URL: devices[0].URL, GuideMode: "xmltv", GuideURL: devices[0].URL + "/guide.xml"}, {ID: "emulator", Name: "HDHomeRun emulator", URL: devices[1].URL, GuideMode: "xmltv", GuideURL: devices[1].URL + "/guide.xml"}}}
	_ = engine.Configure(cfg)
	_ = engine.Refresh(context.Background())
	server := &pluginimpl.Server{Engine: engine}
	var mu sync.Mutex
	values := map[string]any{}
	client := &http.Client{Timeout: 0, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/api/v2/plugin-content/plugins/1/hdhomerun", 302)
			return
		}
		if r.URL.Path == "/api/v2/settings/plugins/1" {
			mu.Lock()
			defer mu.Unlock()
			if r.Method == "PUT" {
				var body struct {
					Values map[string]any `json:"values"`
				}
				if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil {
					w.WriteHeader(400)
					return
				}
				values = body.Values
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"values": values})
			return
		}
		if r.URL.Path == "/api/v2/admin/plugins/installations/1/config" {
			var body struct {
				Key   string
				Value hdhr.Config
			}
			if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil {
				w.WriteHeader(400)
				return
			}
			if err := engine.Configure(body.Value); err != nil {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			engine.RefreshAsync()
			w.WriteHeader(204)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/api/v2/plugin-content/plugins/1")
		q := map[string]any{}
		for k, v := range r.URL.Query() {
			if len(v) > 0 {
				q[k] = v[0]
			}
		}
		query, _ := structpb.NewStruct(q)
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		result, err := server.Handle(r.Context(), &pluginv1.HandleHTTPRequest{Path: p, Method: r.Method, Query: query, Body: body, Headers: map[string]string{"X-Silo-User-Id": "dev", "X-Silo-Profile-Id": "dev-profile", "X-Silo-User-Role": "admin"}})
		if err != nil {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("X-Silo-Stream-Relay", "1")
		if target := result.Headers["X-Silo-Relay-URL"]; target != "" {
			req, err := http.NewRequestWithContext(r.Context(), r.Method, target, nil)
			if err != nil {
				w.WriteHeader(502)
				return
			}
			if v := r.Header.Get("Range"); v != "" {
				req.Header.Set("Range", v)
			}
			up, err := client.Do(req)
			if err != nil {
				w.WriteHeader(502)
				return
			}
			defer up.Body.Close()
			for _, k := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
				if v := up.Header.Get(k); v != "" {
					w.Header().Set(k, v)
				}
			}
			w.WriteHeader(up.StatusCode)
			if r.Method != "HEAD" {
				io.Copy(flushWriter{w}, up.Body)
			}
			return
		}
		for k, v := range result.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(int(result.StatusCode))
		w.Write(result.Body)
	})
	log.Printf("Development UI: http://%s (fixed local admin; two emulators)", *address)
	log.Fatal((&http.Server{Addr: *address, Handler: handler, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe())
}

type flushWriter struct{ http.ResponseWriter }

func (w flushWriter) Write(b []byte) (int, error) {
	n, e := w.ResponseWriter.Write(b)
	w.ResponseWriter.(http.Flusher).Flush()
	return n, e
}
func emulator(index int, media string) *httptest.Server {
	names := []string{"WNYC Local", "Cinema", "Public Television", "World News", "Culture", "Kids"}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discover.json":
			json.NewEncoder(w).Encode(map[string]any{"FriendlyName": "Local emulator", "ModelNumber": "HDHR-EMU", "DeviceID": "same-id", "TunerCount": 2})
		case "/lineup.json":
			rows := []map[string]any{}
			for i, name := range names {
				suffix := "/live.ts"
				if index == 1 {
					suffix = "/media/index.m3u8"
				}
				rows = append(rows, map[string]any{"GuideNumber": fmt.Sprintf("%d.1", i+2), "GuideName": name, "URL": server.URL + suffix})
			}
			json.NewEncoder(w).Encode(rows)
		case "/guide.xml":
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, "<tv>")
			now := time.Now().UTC().Truncate(time.Hour)
			for i, name := range names {
				fmt.Fprintf(w, `<channel id="%d.1"><display-name>%s</display-name></channel>`, i+2, name)
				for j := -2; j < 18; j++ {
					from := now.Add(time.Duration(j) * time.Hour)
					titles := []string{"City stories", "The evening report", "Around the world", "Weekend kitchen", "An American classic", "Science today"}
					title := titles[(i+j+24)%len(titles)]
					fmt.Fprintf(w, `<programme channel="%d.1" start="%s" stop="%s"><title>%s</title><desc>Local development guide. This programme and endpoint are simulated for integration testing.</desc></programme>`, i+2, from.Format("20060102150405 -0700"), from.Add(time.Hour).Format("20060102150405 -0700"), title)
				}
			}
			fmt.Fprint(w, "</tv>")
		case "/live.ts":
			if media == "" {
				w.WriteHeader(503)
				return
			}
			path := filepath.Join(media, "sample.ts")
			w.Header().Set("Content-Type", "video/mp2t")
			for {
				file, err := os.Open(path)
				if err != nil {
					return
				}
				_, err = io.Copy(flushWriter{w}, file)
				file.Close()
				if err != nil {
					return
				}
				select {
				case <-r.Context().Done():
					return
				case <-time.After(time.Second):
				}
			}
		default:
			if media != "" && strings.HasPrefix(r.URL.Path, "/media/") {
				http.StripPrefix("/media/", http.FileServer(http.Dir(media))).ServeHTTP(w, r)
				return
			}
			w.WriteHeader(404)
		}
	}))
	return server
}

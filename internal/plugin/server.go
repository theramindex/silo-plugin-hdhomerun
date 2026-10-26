package plugin

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	sharedhttp "github.com/theramindex/silo-plugins/pkg/livemedia/httpclient"
	mediaweb "github.com/theramindex/silo-plugins/pkg/livemedia/web"
	"html"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/theramindex/silo-plugin-hdhomerun/internal/hdhr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

//go:embed ui/* assets/*
var files embed.FS

var assetVersion = func() string {
	h := sha256.New()
	for _, name := range []string{"ui/index.html", "ui/app.js", "ui/app.css", "ui/theme.js"} {
		b, _ := files.ReadFile(name)
		_, _ = h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}()

func page(r *pluginv1.HandleHTTPRequest) []byte {
	theme := header(r, "X-Silo-Theme")
	if theme == "" {
		theme = query(r, "theme")
	}
	if len(theme) > 64 {
		theme = ""
	}
	for _, c := range theme {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			theme = ""
			break
		}
	}
	prefix := "assets"
	if r.GetPath() == "/hdhomerun" {
		prefix = "hdhomerun/assets"
	}
	class := "is-viewer"
	if r.GetPath() == "/hdhomerun/admin" {
		class = "is-admin"
	}
	b, _ := files.ReadFile("ui/index.html")
	return []byte(strings.NewReplacer("__SILO_THEME__", html.EscapeString(theme), "__ASSET_PREFIX__", prefix, "__ASSET_VERSION__", assetVersion, "__PAGE_CLASS__", class).Replace(string(b)))
}

type Server struct {
	pluginv1.UnimplementedHttpRoutesServer
	pluginv1.UnimplementedScheduledTaskServer
	Engine    *hdhr.Engine
	mediaOnce sync.Once
	media     *mediaweb.Controller
}

func header(r *pluginv1.HandleHTTPRequest, key string) string {
	for k, v := range r.GetHeaders() {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}
func owner(r *pluginv1.HandleHTTPRequest) string {
	uid := header(r, "X-Silo-User-Id")
	if uid == "" {
		return ""
	}
	return uid + "/" + header(r, "X-Silo-Profile-Id")
}
func query(r *pluginv1.HandleHTTPRequest, k string) string {
	v := r.GetQuery().AsMap()[k]
	if a, ok := v.([]any); ok && len(a) > 0 {
		v = a[0]
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
func response(code int, v any) *pluginv1.HandleHTTPResponse {
	b, _ := json.Marshal(v)
	return &pluginv1.HandleHTTPResponse{StatusCode: int32(code), Headers: map[string]string{"Content-Type": "application/json", "Cache-Control": "no-store"}, Body: b}
}
func problem(err error) *pluginv1.HandleHTTPResponse {
	code, label := 400, "bad_request"
	switch {
	case errors.Is(err, hdhr.ErrSessionOwner):
		code, label = 403, "forbidden"
	case errors.Is(err, hdhr.ErrChannelNotFound):
		code, label = 404, "channel_not_found"
	case errors.Is(err, hdhr.ErrSessionNotFound):
		code, label = 404, "session_not_found"
	case errors.Is(err, hdhr.ErrConnectionLimit):
		code, label = 409, "connection_limit"
	case errors.Is(err, hdhr.ErrFormatUnavailable):
		code, label = 422, "format_unavailable"
	case errors.Is(err, hdhr.ErrProtected):
		code, label = 422, "protected_channel"
	}
	return response(code, map[string]string{"code": label, "error": err.Error()})
}
func (s *Server) Handle(ctx context.Context, r *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error) {
	p, m := r.GetPath(), r.GetMethod()
	if mediaweb.Handles("/hdhomerun", p) {
		return s.mediaController().Handle(ctx, r), nil
	}
	isAdmin := header(r, "X-Silo-User-Role") == "admin"
	if owner(r) == "" && !strings.HasPrefix(p, "/silo-compat/livetv/v1/") {
		return response(401, map[string]string{"error": "Sign in to Silo to continue."}), nil
	}
	if strings.HasPrefix(p, "/hdhomerun/admin") && !isAdmin {
		return response(403, map[string]string{"error": "Administrator access required."}), nil
	}
	if m == "GET" && (p == "/hdhomerun" || p == "/hdhomerun/" || p == "/hdhomerun/admin") {
		b := page(r)
		return &pluginv1.HandleHTTPResponse{StatusCode: 200, Headers: map[string]string{"Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store"}, Body: b}, nil
	}
	if m == "GET" && strings.HasPrefix(p, "/hdhomerun/assets/") {
		name := strings.TrimPrefix(p, "/hdhomerun/assets/")
		contentType := "application/javascript; charset=utf-8"
		dir := "ui/"
		if name == "app.css" {
			contentType = "text/css; charset=utf-8"
		} else if name == "hls.min.js" || name == "mpegts.min.js" || name == "hls.LICENSE" || name == "mpegts.LICENSE" || name == "font.LICENSE" || strings.HasPrefix(name, "plus-jakarta-sans-") && strings.HasSuffix(name, ".woff2") {
			dir = "assets/"
			if strings.HasSuffix(name, ".woff2") {
				contentType = "font/woff2"
			}
			if strings.HasSuffix(name, ".LICENSE") {
				contentType = "text/plain; charset=utf-8"
			}
		} else if name != "app.js" && name != "theme.js" {
			return response(404, map[string]string{"error": "Not found"}), nil
		}
		b, err := files.ReadFile(dir + name)
		if err != nil {
			return response(404, nil), nil
		}
		return &pluginv1.HandleHTTPResponse{StatusCode: 200, Headers: map[string]string{"Content-Type": contentType, "Cache-Control": "private, max-age=300"}, Body: b}, nil
	}
	if (m == "GET" || m == "HEAD") && strings.HasPrefix(p, "/hdhomerun/media/") {
		parts := strings.SplitN(strings.TrimPrefix(p, "/hdhomerun/media/"), "/", 2)
		if len(parts) != 2 {
			return response(404, nil), nil
		}
		target, err := s.Engine.RelayURL(parts[0], owner(r), parts[1])
		if err != nil {
			return problem(err), nil
		}
		return &pluginv1.HandleHTTPResponse{StatusCode: 200, Headers: map[string]string{"X-Silo-Relay-URL": target}}, nil
	}
	switch {
	case m == "GET" && p == "/hdhomerun/api/state":
		snap := s.Engine.Snapshot()
		return response(200, map[string]any{"channels": snap.Channels, "endpoints": snap.Endpoints, "refreshing": snap.Refreshing, "updatedAtUnix": snap.UpdatedAtUnix, "admin": isAdmin}), nil
	case m == "GET" && p == "/hdhomerun/admin/api/config":
		return response(200, s.Engine.Config()), nil
	case m == "POST" && p == "/hdhomerun/admin/api/validate":
		if len(r.GetBody()) > 1<<20 {
			return response(413, nil), nil
		}
		var cfg hdhr.Config
		if err := json.Unmarshal(r.GetBody(), &cfg); err != nil {
			return response(400, map[string]string{"error": "Invalid endpoint configuration."}), nil
		}
		normalized, err := cfg.Normalize()
		if err != nil {
			return problem(err), nil
		}
		return response(200, normalized), nil
	case m == "POST" && p == "/hdhomerun/admin/api/refresh":
		s.Engine.RefreshAsync()
		return response(202, map[string]bool{"queued": true}), nil
	case m == "GET" && p == "/silo-compat/livetv/v1/info":
		return response(200, map[string]any{"contractVersion": 1, "name": "HDHomeRun", "streamHosts": []string{s.Engine.StreamHost()}}), nil
	case m == "GET" && p == "/silo-compat/livetv/v1/channels":
		return response(200, s.Engine.Snapshot().Channels), nil
	case m == "GET" && (p == "/silo-compat/livetv/v1/programs" || p == "/hdhomerun/api/guide"):
		return s.programs(r), nil
	case (m == "GET" && p == "/silo-compat/livetv/v1/stream") || (m == "POST" && p == "/hdhomerun/api/play"):
		channel, session, format := query(r, "channel_id"), query(r, "session_id"), query(r, "format")
		browser := p == "/hdhomerun/api/play"
		if browser {
			var b struct {
				ChannelID string `json:"channelId"`
				SessionID string `json:"sessionId"`
				Format    string `json:"format"`
			}
			if len(r.GetBody()) > 4096 || json.Unmarshal(r.GetBody(), &b) != nil {
				return response(400, nil), nil
			}
			channel, session, format = b.ChannelID, b.SessionID, b.Format
		}
		tracked := session != ""
		if session == "" {
			session = "ephemeral-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		}
		stream, err := s.Engine.Resolve(channel, session, owner(r), format)
		if err != nil {
			return problem(err), nil
		}
		stream.Leased = tracked
		if browser {
			u, _ := url.Parse(stream.URL)
			return response(200, map[string]any{"path": "/hdhomerun/media/" + strings.TrimPrefix(u.Path, "/stream/"), "sessionId": session, "container": stream.Container}), nil
		}
		return response(200, stream), nil
	case m == "POST" && (strings.HasSuffix(p, "/session/heartbeat") || strings.HasSuffix(p, "/session/stop")) && (strings.HasPrefix(p, "/silo-compat/livetv/v1/") || strings.HasPrefix(p, "/hdhomerun/api/")):
		id := query(r, "session_id")
		var err error
		if strings.HasSuffix(p, "/stop") {
			err = s.Engine.Stop(id, owner(r))
		} else {
			err = s.Engine.Heartbeat(id, owner(r))
		}
		if err != nil {
			return problem(err), nil
		}
		return response(200, map[string]any{}), nil
	}
	return response(404, map[string]string{"error": "Not found"}), nil
}
func (s *Server) programs(r *pluginv1.HandleHTTPRequest) *pluginv1.HandleHTTPResponse {
	start, e1 := strconv.ParseInt(query(r, "start"), 10, 64)
	end, e2 := strconv.ParseInt(query(r, "end"), 10, 64)
	class := query(r, "class")
	if e1 != nil || e2 != nil || end <= start || end-start > 16*86400 {
		return response(400, map[string]string{"code": "bad_request"})
	}
	if class != "" && class != "movie" && class != "sports" && class != "news" && class != "kids" && class != "series" {
		return response(400, map[string]string{"code": "bad_request"})
	}
	ids := []string{}
	if raw := query(r, "channel_ids"); raw != "" {
		ids = strings.Split(raw, ",")
		if len(ids) > 2000 {
			return response(400, nil)
		}
	}
	snap := s.Engine.Snapshot()
	rank := map[string]int{}
	if len(ids) == 0 {
		for i, c := range snap.Channels {
			rank[c.ID] = i
		}
	} else {
		for i, id := range ids {
			rank[id] = i
		}
	}
	limit := 200
	if len(ids) > 0 {
		limit = 100000
	}
	if n, err := strconv.Atoi(query(r, "limit")); err == nil && n > 0 {
		limit = n
	}
	if len(ids) == 0 && limit > 2000 {
		limit = 2000
	}
	if limit > 100000 {
		limit = 100000
	}
	out := []hdhr.Program{}
	for _, p := range snap.Programs {
		if _, ok := rank[p.ChannelID]; !ok || p.StartUnix >= end || p.EndUnix <= start {
			continue
		}
		if class != "" && !((class == "movie" && p.IsMovie) || (class == "sports" && p.IsSports) || (class == "news" && p.IsNews) || (class == "kids" && p.IsKids) || (class == "series" && p.IsSeries)) {
			continue
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].ChannelID] != rank[out[j].ChannelID] {
			return rank[out[i].ChannelID] < rank[out[j].ChannelID]
		}
		return out[i].StartUnix < out[j].StartUnix
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return response(200, map[string]any{"programs": out})
}
func (s *Server) Run(ctx context.Context, r *pluginv1.RunScheduledTaskRequest) (*pluginv1.RunScheduledTaskResponse, error) {
	if r.GetTaskKey() != "hdhomerun-refresh" {
		return nil, status.Error(codes.NotFound, "unknown task")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := s.Engine.Refresh(ctx); err != nil {
		return nil, status.Error(codes.Unavailable, "refresh did not finish")
	}
	snap := s.Engine.Snapshot()
	out, _ := structpb.NewStruct(map[string]any{"channels": len(snap.Channels), "endpoints": len(snap.Endpoints)})
	return &pluginv1.RunScheduledTaskResponse{Output: out}, nil
}

func (s *Server) mediaController() *mediaweb.Controller {
	s.mediaOnce.Do(func() {
		s.media = mediaweb.New(mediaweb.Config{Prefix: "/hdhomerun", Root: filepath.Join(s.Engine.DataDir(), "media"), Resolve: func(ctx context.Context, channel, viewer, id string) (mediaweb.Input, error) {
			stream, err := s.Engine.Resolve(channel, id, viewer, "any")
			if err != nil {
				return mediaweb.Input{}, err
			}
			client := sharedhttp.NewRestricted("127.0.0.1")
			client.Timeout = 0
			if tr, ok := client.Transport.(*http.Transport); ok {
				tr.ResponseHeaderTimeout = 10 * time.Second
			}
			return mediaweb.Input{URL: stream.URL, Container: stream.Container, Client: client, Release: func() { s.Engine.Stop(id, viewer) }, Heartbeat: func() error { return s.Engine.Heartbeat(id, viewer) }}, nil
		}})
	})
	return s.media
}

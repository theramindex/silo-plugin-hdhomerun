package hdhr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

var ErrProtected = errors.New("this channel requires content protection")
var hlsURI = regexp.MustCompile(`URI="([^"]+)"`)

func (e *Engine) Resolve(channelID, sessionID, owner, format string) (Stream, error) {
	if sessionID == "" || len(sessionID) > 128 || strings.ContainsAny(sessionID, "\r\n\x00") || owner == "" {
		return Stream{}, errors.New("a valid playback session and viewer are required")
	}
	if format == "" {
		format = "any"
	}
	if format != "any" && format != "ts" && format != "hls" {
		return Stream{}, ErrFormatUnavailable
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expireLocked()
	if e.closed {
		return Stream{}, errors.New("plugin is stopping")
	}
	if l := e.leases[sessionID]; l != nil {
		if l.Owner != owner {
			return Stream{}, ErrSessionOwner
		}
		if l.ChannelID != channelID {
			return Stream{}, errors.New("session is already playing another channel")
		}
		if format != "any" && l.Format != format {
			return Stream{}, ErrFormatUnavailable
		}
		l.Expires = time.Now().Add(LeaseTTL)
		return e.streamLocked(l), nil
	}
	ch := e.channelLocked(channelID)
	if ch == nil {
		return Stream{}, ErrChannelNotFound
	}
	if ch.Protected {
		return Stream{}, ErrProtected
	}
	if len(e.leases) >= maxLeases {
		return Stream{}, ErrConnectionLimit
	}
	l := &lease{ID: sessionID, Owner: owner, ChannelID: channelID, Format: format, Expires: time.Now().Add(LeaseTTL), resources: map[string]string{}, reverse: map[string]string{}, resourceSeen: map[string]time.Time{}}
	src, ok := e.selectSourceLocked(ch, l, nil)
	if !ok {
		if !hasFormat(ch, format) {
			return Stream{}, ErrFormatUnavailable
		}
		return Stream{}, ErrConnectionLimit
	}
	token, err := randomToken()
	if err != nil {
		return Stream{}, errors.New("unable to create playback session")
	}
	l.ctx, l.cancel = context.WithCancel(e.ctx)
	l.Token = token
	l.EndpointID = src.EndpointID
	l.Format = src.Container
	e.leases[sessionID] = l
	return e.streamLocked(l), nil
}
func (e *Engine) channelLocked(id string) *Channel {
	for i := range e.channels {
		if e.channels[i].ID == id {
			return &e.channels[i]
		}
	}
	return nil
}
func hasFormat(ch *Channel, format string) bool {
	for _, s := range ch.sources {
		if !s.Protected && (format == "any" || s.Container == format) {
			return true
		}
	}
	return false
}
func (e *Engine) selectSourceLocked(ch *Channel, l *lease, excluded map[string]bool) (source, bool) {
	for pass := 0; pass < 2; pass++ {
		for _, src := range ch.sources {
			if src.Protected || excluded[src.EndpointID] || (l.Format != "any" && src.Container != l.Format) {
				continue
			}
			d, ok := e.devices[src.EndpointID]
			if !ok || d.client == nil {
				continue
			}
			if pass == 0 && (d.Error != "" || time.Now().Before(e.cooldown[src.EndpointID])) {
				continue
			}
			var ep Endpoint
			found := false
			for _, configured := range e.config.Endpoints {
				if configured.ID == src.EndpointID && configured.IsEnabled() {
					ep = configured
					found = true
					break
				}
			}
			if !found {
				continue
			}
			used := 0
			for _, other := range e.leases {
				if other != l && other.EndpointID == src.EndpointID {
					used++
				}
			}
			if used < tunerLimit(ep, d) {
				return src, true
			}
		}
	}
	return source{}, false
}
func (e *Engine) streamLocked(l *lease) Stream {
	file := "live.ts"
	if l.Format == "hls" {
		file = "index.m3u8"
	}
	return Stream{URL: "http://" + e.address + "/stream/" + l.Token + "/" + file, Container: l.Format, Mode: "server", Leased: true}
}
func (e *Engine) Heartbeat(id, owner string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expireLocked()
	l := e.leases[id]
	if l == nil {
		return ErrSessionNotFound
	}
	if l.Owner != owner {
		return ErrSessionOwner
	}
	l.Expires = time.Now().Add(LeaseTTL)
	return nil
}
func (e *Engine) Stop(id, owner string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	l := e.leases[id]
	if l == nil {
		return nil
	}
	if l.Owner != owner {
		return ErrSessionOwner
	}
	if l.cancel != nil {
		l.cancel()
	}
	delete(e.leases, id)
	return nil
}
func randomToken() (string, error) {
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}
func (e *Engine) tokenLocked(token string) *lease {
	for _, l := range e.leases {
		if l.Token == token {
			return l
		}
	}
	return nil
}
func (e *Engine) finish(token string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if l := e.tokenLocked(token); l != nil {
		if l.cancel != nil {
			l.cancel()
		}
		delete(e.leases, l.ID)
	}
}

func (e *Engine) serveGateway(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(405)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || len(parts) > 4 || parts[0] != "stream" || len(parts[1]) != 48 {
		http.NotFound(w, r)
		return
	}
	token := parts[1]
	e.mu.Lock()
	e.expireLocked()
	l := e.tokenLocked(token)
	if l == nil {
		e.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	format := l.Format
	isRoot := (format == "ts" && len(parts) == 3 && parts[2] == "live.ts") || (format == "hls" && len(parts) == 3 && parts[2] == "index.m3u8")
	asset := ""
	if format == "hls" && len(parts) == 4 && parts[2] == "asset" {
		asset = l.resources[parts[3]]
		l.resourceSeen[parts[3]] = time.Now()
	}
	if !isRoot && asset == "" {
		e.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	// HEAD checks must not tune or consume a physical tuner.
	if r.Method == http.MethodHead {
		e.mu.Unlock()
		if format == "hls" {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		} else {
			w.Header().Set("Content-Type", "video/mp2t")
		}
		w.WriteHeader(200)
		return
	}
	if format == "ts" && l.active {
		e.mu.Unlock()
		http.Error(w, "playback session is already streaming", 409)
		return
	}
	l.Expires = time.Now().Add(LeaseTTL)
	if format == "ts" {
		l.active = true
	}
	e.mu.Unlock()
	if format == "ts" {
		e.serveTS(w, r, token)
		return
	}
	e.serveHLS(w, r, token, asset)
}
func (e *Engine) openStream(ctx context.Context, token string) (*http.Response, *DeviceClient, error) {
	// Bound initial tuner selection without imposing a lifetime on the stream.
	ctx, cancel := context.WithCancel(ctx)
	deadline := time.AfterFunc(12*time.Second, cancel)
	defer deadline.Stop()
	failed := true
	defer func() {
		if failed {
			cancel()
		}
	}()
	excluded := map[string]bool{}
	for attempts := 0; attempts < MaxEndpoints; attempts++ {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		e.mu.Lock()
		l := e.tokenLocked(token)
		if l == nil {
			e.mu.Unlock()
			return nil, nil, ErrSessionNotFound
		}
		ch := e.channelLocked(l.ChannelID)
		if ch == nil {
			e.mu.Unlock()
			return nil, nil, ErrChannelNotFound
		}
		// The selected endpoint is a reservation, not another stream connection.
		// Keep it when healthy; on a failure move the slot atomically to a fallback.
		var src source
		var ok bool
		for _, candidate := range ch.sources {
			if candidate.EndpointID == l.EndpointID && candidate.Container == l.Format && !candidate.Protected && !excluded[candidate.EndpointID] {
				src = candidate
				ok = true
				break
			}
		}
		if !ok {
			src, ok = e.selectSourceLocked(ch, l, excluded)
		}
		if !ok {
			e.mu.Unlock()
			return nil, nil, ErrConnectionLimit
		}
		l.EndpointID = src.EndpointID
		client := e.devices[src.EndpointID].client
		e.mu.Unlock()
		if client == nil {
			return nil, nil, errors.New("source is not ready")
		}
		response, err := client.Get(ctx, src.URL)
		if err == nil && response.StatusCode == 200 {
			deadline.Stop()
			failed = false
			response.Body = &cancelBody{ReadCloser: response.Body, cancel: cancel}
			return response, client, nil
		}
		if response != nil {
			_ = response.Body.Close()
		}
		e.mu.Lock()
		e.cooldown[src.EndpointID] = time.Now().Add(15 * time.Second)
		e.mu.Unlock()
		excluded[src.EndpointID] = true
	}
	return nil, nil, errors.New("no source could start this channel")
}
func (e *Engine) serveTS(w http.ResponseWriter, r *http.Request, token string) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer e.finish(token)
	e.mu.Lock()
	l := e.tokenLocked(token)
	if l == nil {
		e.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	stopLease := context.AfterFunc(l.ctx, cancel)
	defer stopLease()
	e.mu.Unlock()
	response, _, err := e.openStream(ctx, token)
	if err != nil {
		http.Error(w, "No available tuner could start this channel.", 503)
		return
	}
	defer response.Body.Close()
	// Bind the lifetime to both the HTTP reader and the plugin lifecycle.
	stop := context.AfterFunc(e.ctx, cancel)
	defer stop()
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				e.mu.Lock()
				if l := e.tokenLocked(token); l != nil {
					l.Expires = time.Now().Add(LeaseTTL)
				}
				e.mu.Unlock()
			}
		}
	}()
	w.Header().Set("Content-Type", "video/mp2t")
	w.WriteHeader(200)
	_, _ = io.CopyBuffer(flushingWriter{w}, response.Body, make([]byte, 32<<10))
}

type flushingWriter struct{ http.ResponseWriter }

func (w flushingWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
}
func (e *Engine) serveHLS(w http.ResponseWriter, r *http.Request, token, asset string) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	stop := context.AfterFunc(e.ctx, cancel)
	defer stop()
	e.mu.Lock()
	l := e.tokenLocked(token)
	if l == nil {
		e.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	stopLease := context.AfterFunc(l.ctx, cancel)
	e.mu.Unlock()
	defer stopLease()
	var response *http.Response
	var client *DeviceClient
	var err error
	if asset == "" {
		response, client, err = e.openStream(ctx, token)
	} else {
		e.mu.Lock()
		l := e.tokenLocked(token)
		if l != nil {
			client = e.devices[l.EndpointID].client
		}
		e.mu.Unlock()
		if client == nil {
			http.NotFound(w, r)
			return
		}
		response, err = client.GetRange(ctx, asset, r.Header.Get("Range"))
	}
	if err != nil || response == nil {
		if asset == "" {
			e.finish(token)
		}
		http.Error(w, "Unable to read this live stream.", 503)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 && response.StatusCode != 206 {
		http.Error(w, "Live stream resource is unavailable.", 503)
		return
	}
	isPlaylist := asset == "" || strings.HasSuffix(strings.ToLower(response.Request.URL.Path), ".m3u8") || strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "mpegurl")
	if isPlaylist {
		data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		if err != nil || len(data) > 2<<20 {
			http.Error(w, "Invalid live playlist.", 502)
			return
		}
		rewritten, err := e.rewritePlaylist(token, response.Request.URL, client, string(data), r.URL.Query().Get("relative"))
		if err != nil {
			http.Error(w, "Invalid or unapproved live playlist resource.", 502)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = io.WriteString(w, rewritten)
		return
	}
	contentType := "video/mp2t"
	switch strings.ToLower(path.Ext(response.Request.URL.Path)) {
	case ".m4s", ".mp4":
		contentType = "video/mp4"
	case ".key":
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	for _, key := range []string{"Content-Range", "Accept-Ranges"} {
		if value := response.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(response.Body, 32<<20))
}
func (e *Engine) rewritePlaylist(token string, base *url.URL, client *DeviceClient, body, relative string) (string, error) {
	if !strings.HasPrefix(strings.TrimSpace(body), "#EXTM3U") {
		return "", errors.New("not an HLS playlist")
	}
	var rewriteErr error
	replace := func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			rewriteErr = err
			return ""
		}
		u = base.ResolveReference(u)
		if err = client.Check(u); err != nil {
			rewriteErr = err
			return ""
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		l := e.tokenLocked(token)
		if l == nil {
			rewriteErr = ErrSessionNotFound
			return ""
		}
		for key, seen := range l.resourceSeen {
			if time.Since(seen) > 3*time.Minute {
				delete(l.reverse, l.resources[key])
				delete(l.resources, key)
				delete(l.resourceSeen, key)
			}
		}
		key := l.reverse[u.String()]
		if key == "" {
			if len(l.resources) >= 2048 {
				rewriteErr = errors.New("playlist resource limit reached")
				return ""
			}
			key, err = randomToken()
			if err != nil {
				rewriteErr = err
				return ""
			}
			l.resources[key] = u.String()
			l.reverse[u.String()] = key
		}
		l.resourceSeen[key] = time.Now()
		if relative == "root" {
			return "asset/" + key
		}
		if relative == "asset" {
			return key
		}
		return "http://" + e.address + "/stream/" + token + "/asset/" + key
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			lines[i] = hlsURI.ReplaceAllStringFunc(line, func(value string) string {
				raw := hlsURI.FindStringSubmatch(value)[1]
				return `URI="` + replace(raw) + `"`
			})
		} else {
			lines[i] = replace(trimmed)
		}
	}
	if rewriteErr != nil {
		return "", rewriteErr
	}
	return strings.Join(lines, "\n"), nil
}

// RelayURL admits only resources issued to this viewer. The host consumes the
// returned loopback URL; browser clients receive only their authenticated route.
func (e *Engine) RelayURL(token, owner, suffix string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expireLocked()
	l := e.tokenLocked(token)
	if l == nil {
		return "", ErrSessionNotFound
	}
	if l.Owner != owner {
		return "", ErrSessionOwner
	}
	relative := ""
	if suffix == "live.ts" && l.Format == "ts" {
	} else if suffix == "index.m3u8" && l.Format == "hls" {
		relative = "root"
	} else if strings.HasPrefix(suffix, "asset/") && l.Format == "hls" && l.resources[strings.TrimPrefix(suffix, "asset/")] != "" {
		relative = "asset"
	} else {
		return "", ErrSessionNotFound
	}
	target := "http://" + e.address + "/stream/" + token + "/" + suffix
	if relative != "" {
		target += "?relative=" + relative
	}
	return target, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error { b.cancel(); return b.ReadCloser.Close() }

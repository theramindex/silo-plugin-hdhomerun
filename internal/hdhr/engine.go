package hdhr

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrChannelNotFound   = errors.New("channel not found")
	ErrConnectionLimit   = errors.New("all available tuners are in use")
	ErrFormatUnavailable = errors.New("the requested stream format is unavailable")
	ErrSessionNotFound   = errors.New("session not found")
	ErrSessionOwner      = errors.New("session belongs to a different viewer")
)

const LeaseTTL = 90 * time.Second
const maxLeases = 1024

type Channel struct {
	ID        string       `json:"id"`
	Number    string       `json:"number"`
	Name      string       `json:"name"`
	LogoURL   string       `json:"logoUrl,omitempty"`
	GroupID   string       `json:"groupId"`
	Group     string       `json:"group"`
	Folders   []string     `json:"folders,omitempty"`
	Sources   []SourceInfo `json:"sources"`
	Protected bool         `json:"protected,omitempty"`
	sources   []source
}
type SourceInfo struct {
	EndpointID string `json:"endpointId"`
	Name       string `json:"name"`
	Number     string `json:"number"`
}
type source struct {
	EndpointID, Number, URL, Container, GuideID string
	Protected                                   bool
	Priority                                    int
}
type EndpointStatus struct {
	CapacityGroup    string `json:"capacityGroup,omitempty"`
	SharedInUse      int    `json:"sharedInUse,omitempty"`
	SharedTunerLimit int    `json:"sharedTunerLimit,omitempty"`
	ID               string `json:"id"`
	Name             string `json:"name"`
	Enabled          bool   `json:"enabled"`
	DeviceID         string `json:"deviceId,omitempty"`
	Model            string `json:"model,omitempty"`
	TunerCount       int    `json:"tunerCount"`
	InUse            int    `json:"inUse"`
	ChannelCount     int    `json:"channelCount"`
	ProgramCount     int    `json:"programCount"`
	LastSuccessUnix  int64  `json:"lastSuccessUnix,omitempty"`
	Error            string `json:"error,omitempty"`
	GuideError       string `json:"guideError,omitempty"`
}
type Snapshot struct {
	Channels      []Channel        `json:"channels"`
	Programs      []Program        `json:"programs"`
	Endpoints     []EndpointStatus `json:"endpoints"`
	Refreshing    bool             `json:"refreshing"`
	UpdatedAtUnix int64            `json:"updatedAtUnix"`
	GatewayReady  bool             `json:"gatewayReady"`
}
type deviceState struct {
	Discovery       Discovery       `json:"discovery"`
	Lineup          []LineupChannel `json:"lineup"`
	Guide           Guide           `json:"guide"`
	LastSuccessUnix int64           `json:"lastSuccessUnix"`
	NextGuideUnix   int64           `json:"nextGuideUnix"`
	Error           string          `json:"error,omitempty"`
	GuideError      string          `json:"guideError,omitempty"`
	client          *DeviceClient
}
type diskState struct {
	Signature     string                 `json:"signature"`
	Devices       map[string]deviceState `json:"devices"`
	UpdatedAtUnix int64                  `json:"updatedAtUnix"`
}
type lease struct {
	ID, Owner, ChannelID, Token, EndpointID, Format string
	Expires                                         time.Time
	active                                          bool
	ctx                                             context.Context
	cancel                                          context.CancelFunc
	resources                                       map[string]string
	reverse                                         map[string]string
	resourceSeen                                    map[string]time.Time
}
type Stream struct {
	URL       string `json:"url"`
	Container string `json:"container"`
	Mode      string `json:"mode"`
	Leased    bool   `json:"leased"`
}

type Engine struct {
	mu           sync.Mutex
	refreshMu    sync.Mutex
	config       Config
	signature    string
	devices      map[string]deviceState
	channels     []Channel
	programs     []Program
	updatedAt    int64
	refreshing   bool
	closed       bool
	leases       map[string]*lease
	cooldown     map[string]time.Time
	dir, address string
	server       *http.Server
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

func NewEngine(dir string) (*Engine, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("unable to start local stream relay")
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{config: Config{Endpoints: []Endpoint{}}, devices: map[string]deviceState{}, leases: map[string]*lease{}, cooldown: map[string]time.Time{}, dir: dir, address: listener.Addr().String(), ctx: ctx, cancel: cancel}
	e.server = &http.Server{Handler: http.HandlerFunc(e.serveGateway), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	e.wg.Add(2)
	go func() { defer e.wg.Done(); _ = e.server.Serve(listener) }()
	go e.background()
	return e, nil
}
func (e *Engine) Close() {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.cancel()
	_ = e.server.Close()
	e.mu.Lock()
	e.stopAllLocked()
	for _, d := range e.devices {
		if d.client != nil {
			d.client.Close()
		}
	}
	e.mu.Unlock()
	e.wg.Wait()
}
func (e *Engine) background() {
	defer e.wg.Done()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.mu.Lock()
			e.expireLocked()
			due := len(e.config.Endpoints) > 0 && time.Since(time.Unix(e.updatedAt, 0)) > 5*time.Minute
			e.mu.Unlock()
			if due {
				e.RefreshAsync()
			}
		}
	}
}
func (e *Engine) Configure(cfg Config) error {
	cfg, err := cfg.Normalize()
	if err != nil {
		return err
	}
	data, _ := json.Marshal(cfg)
	digest := sha256.Sum256(data)
	signature := hex.EncodeToString(digest[:])
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return errors.New("plugin is stopping")
	}
	if signature == e.signature {
		e.mu.Unlock()
		return nil
	}
	e.stopAllLocked()
	for _, d := range e.devices {
		if d.client != nil {
			d.client.Close()
		}
	}
	e.config = cfg
	e.signature = signature
	e.devices = map[string]deviceState{}
	e.updatedAt = 0
	e.channels = nil
	e.programs = nil
	// Cache contains private source URLs. It is never served through user APIs.
	if e.dir != "" {
		if info, err := os.Stat(filepath.Join(e.dir, "catalog.json")); err == nil && info.Size() <= 64<<20 {
			if data, err := os.ReadFile(filepath.Join(e.dir, "catalog.json")); err == nil {
				var saved diskState
				if json.Unmarshal(data, &saved) == nil && saved.Signature == signature {
					e.devices = saved.Devices
					e.updatedAt = saved.UpdatedAtUnix
					if e.devices == nil {
						e.devices = map[string]deviceState{}
					}
				}
			}
		}
	}
	e.mu.Unlock()
	// No network access inside Configure: the host has a short RPC deadline.
	return nil
}
func (e *Engine) Config() Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	data, _ := json.Marshal(e.config)
	var cfg Config
	_ = json.Unmarshal(data, &cfg)
	return cfg
}
func (e *Engine) RefreshAsync() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.wg.Add(1)
	e.mu.Unlock()
	go func() {
		defer e.wg.Done()
		ctx, cancel := context.WithTimeout(e.ctx, 45*time.Second)
		defer cancel()
		_ = e.Refresh(ctx)
	}()
}
func (e *Engine) Refresh(ctx context.Context) error {
	if !e.refreshMu.TryLock() {
		return nil
	}
	defer e.refreshMu.Unlock()
	e.mu.Lock()
	cfg := e.config
	signature := e.signature
	old := make(map[string]deviceState, len(e.devices))
	for id, d := range e.devices {
		old[id] = d
	}
	e.refreshing = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.refreshing = false; e.mu.Unlock() }()
	states := make(map[string]deviceState, len(cfg.Endpoints))
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for _, endpoint := range cfg.Endpoints {
		endpoint := endpoint
		if !endpoint.IsEnabled() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				return
			}
			d := old[endpoint.ID]
			client, err := NewDeviceClient(ctx, endpoint)
			if err == nil {
				d.client = client
				var discovery Discovery
				discovery, err = client.Discover(ctx)
				if err == nil {
					var lineup []LineupChannel
					lineup, err = client.Lineup(ctx, discovery)
					if err == nil {
						d.Discovery = discovery
						d.Lineup = lineup
						d.Error = ""
						d.LastSuccessUnix = time.Now().Unix()
						if endpoint.GuideMode != "none" && d.NextGuideUnix <= time.Now().Unix() {
							guide, delay, guideErr := fetchGuide(ctx, endpoint, discovery, lineup)
							if guideErr == nil {
								d.Guide = guide
								d.GuideError = ""
								d.NextGuideUnix = time.Now().Add(delay).Unix()
							} else {
								d.GuideError = guideErr.Error()
								d.NextGuideUnix = time.Now().Add(30 * time.Minute).Unix()
							}
						}
						if endpoint.GuideMode == "none" {
							d.Guide = Guide{}
							d.GuideError = ""
						}
					}
				}
			}
			d.Discovery.DeviceAuth = ""
			if err != nil {
				d.Error = err.Error()
			}
			mu.Lock()
			states[endpoint.ID] = d
			mu.Unlock()
		}()
	}
	wg.Wait()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.signature != signature || e.closed {
		for _, d := range states {
			if d.client != nil {
				d.client.Close()
			}
		}
		return errors.New("configuration changed during refresh")
	}
	for _, d := range e.devices {
		if d.client != nil {
			d.client.Close()
		}
	}
	e.devices = states
	e.updatedAt = time.Now().Unix()
	e.rebuildLocked()
	e.persistLocked()
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
func (e *Engine) persistLocked() {
	if e.dir == "" {
		return
	}
	if os.MkdirAll(e.dir, 0700) != nil {
		return
	}
	data, err := json.Marshal(diskState{Signature: e.signature, Devices: e.devices, UpdatedAtUnix: e.updatedAt})
	if err != nil || len(data) > 64<<20 {
		return
	}
	f, err := os.CreateTemp(e.dir, ".catalog-*")
	if err != nil {
		return
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return
	}
	if f.Close() != nil {
		return
	}
	_ = os.Rename(name, filepath.Join(e.dir, "catalog.json"))
}
func (e *Engine) rebuildLocked() {
	channels := []Channel{}
	programs := []Program{}
	byID := map[string]int{}
	endpoints := append([]Endpoint{}, e.config.Endpoints...)
	sort.SliceStable(endpoints, func(i, j int) bool { return endpoints[i].Priority < endpoints[j].Priority })
	for _, endpoint := range endpoints {
		if !endpoint.IsEnabled() {
			continue
		}
		d := e.devices[endpoint.ID]
		if d.client == nil {
			continue
		}
		if d.Guide.names == nil {
			d.Guide.names = guideNames(d.Guide.Channels)
			e.devices[endpoint.ID] = d
		}
		guidePrograms := map[string][]Program{}
		for _, program := range d.Guide.Programs {
			guidePrograms[program.ChannelID] = append(guidePrograms[program.ChannelID], program)
		}
		seenNumbers := map[string]bool{}
		for _, raw := range d.Lineup {
			number := strings.TrimSpace(raw.Number)
			name := strings.TrimSpace(raw.Name)
			if number == "" || len(number) > 64 || name == "" || len(name) > 200 || seenNumbers[number] {
				continue
			}
			seenNumbers[number] = true
			mapping := endpoint.Channels[number]
			if mapping.Hidden {
				continue
			}
			if mapping.Name != "" {
				name = mapping.Name
			}
			stream, err := d.client.Resolve(raw.URL)
			if err != nil || raw.URL == "" {
				continue
			}
			id := "hdhr:" + endpoint.ID + ":" + base64.RawURLEncoding.EncodeToString([]byte(number))
			groupID := "endpoint:" + endpoint.ID
			group := endpoint.Name
			if mapping.StationID != "" {
				id = "station:" + mapping.StationID
				groupID = "combined"
				group = "Combined channels"
			}
			container := "ts"
			u, _ := ParseHTTPURL(stream)
			if strings.HasSuffix(strings.ToLower(u.Path), ".m3u8") {
				container = "hls"
			}
			guideID := mapping.GuideID
			if guideID == "" {
				guideID = d.Guide.MatchChannel(raw)
			}
			src := source{EndpointID: endpoint.ID, Number: number, URL: stream, Container: container, Protected: raw.Protected(), GuideID: guideID, Priority: endpoint.Priority}
			pos, exists := byID[id]
			if !exists {
				pos = len(channels)
				byID[id] = pos
				channels = append(channels, Channel{ID: id, Number: number, Name: name, GroupID: groupID, Group: group, Folders: []string{group}, Sources: []SourceInfo{}, Protected: true})
			}
			ch := &channels[pos]
			ch.sources = append(ch.sources, src)
			ch.Sources = append(ch.Sources, SourceInfo{EndpointID: endpoint.ID, Name: endpoint.Name, Number: number})
			ch.Protected = ch.Protected && src.Protected
			if guideID != "" {
				if ch.LogoURL == "" {
					ch.LogoURL = d.Guide.Logos[guideID]
				}
				for _, gp := range guidePrograms[guideID] {
					programs = append(programs, gp.ForChannel(id))
				}
			}
		}
	}
	for i := range channels {
		sort.SliceStable(channels[i].sources, func(a, b int) bool { return channels[i].sources[a].Priority < channels[i].sources[b].Priority })
	}
	// Prefer the guide of the highest-priority endpoint for explicitly merged stations.
	seenPrograms := map[string]bool{}
	unique := []Program{}
	for _, p := range programs {
		key := fmt.Sprintf("%s:%d", p.ChannelID, p.StartUnix)
		if !seenPrograms[key] {
			seenPrograms[key] = true
			unique = append(unique, p)
		}
	}
	sort.SliceStable(unique, func(i, j int) bool {
		if unique[i].ChannelID != unique[j].ChannelID {
			return unique[i].ChannelID < unique[j].ChannelID
		}
		return unique[i].StartUnix < unique[j].StartUnix
	})
	e.channels = channels
	e.programs = unique
}
func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expireLocked()
	out := Snapshot{Channels: []Channel{}, Programs: append([]Program{}, e.programs...), Endpoints: []EndpointStatus{}, Refreshing: e.refreshing, UpdatedAtUnix: e.updatedAt, GatewayReady: true}
	for _, ch := range e.channels {
		ch.sources = nil
		ch.Sources = append([]SourceInfo{}, ch.Sources...)
		ch.Folders = append([]string{}, ch.Folders...)
		out.Channels = append(out.Channels, ch)
	}
	for _, endpoint := range e.config.Endpoints {
		d := e.devices[endpoint.ID]
		status := EndpointStatus{ID: endpoint.ID, Name: endpoint.Name, Enabled: endpoint.IsEnabled(), DeviceID: d.Discovery.DeviceID, Model: d.Discovery.ModelNumber, TunerCount: tunerLimit(endpoint, d), ChannelCount: len(d.Lineup), ProgramCount: len(d.Guide.Programs), LastSuccessUnix: d.LastSuccessUnix, Error: d.Error, GuideError: d.GuideError}
		for _, l := range e.leases {
			if l.EndpointID == endpoint.ID {
				status.InUse++
			}
		}
		if endpoint.CapacityGroup != "" {
			status.CapacityGroup = endpoint.CapacityGroup
			status.SharedInUse, status.SharedTunerLimit = e.sharedCapacityUsageLocked(endpoint, nil)
		}
		out.Endpoints = append(out.Endpoints, status)
	}
	return out
}
func tunerLimit(endpoint Endpoint, d deviceState) int {
	if endpoint.TunerLimit > 0 {
		return endpoint.TunerLimit
	}
	if d.Discovery.TunerCount > 0 && d.Discovery.TunerCount <= 64 {
		return d.Discovery.TunerCount
	}
	return 1
}
func (e *Engine) StreamHost() string { return e.address }
func (e *Engine) stopAllLocked() {
	for _, l := range e.leases {
		if l.cancel != nil {
			l.cancel()
		}
	}
	e.leases = map[string]*lease{}
}
func (e *Engine) expireLocked() {
	now := time.Now()
	for id, l := range e.leases {
		if !now.Before(l.Expires) {
			if l.cancel != nil {
				l.cancel()
			}
			delete(e.leases, id)
		}
	}
}

func (e *Engine) DataDir() string { return e.dir }

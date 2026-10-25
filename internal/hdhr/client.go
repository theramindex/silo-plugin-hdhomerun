package hdhr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

type Discovery struct {
	FriendlyName string `json:"FriendlyName"`
	ModelNumber  string `json:"ModelNumber"`
	DeviceID     string `json:"DeviceID"`
	DeviceAuth   string `json:"DeviceAuth"`
	TunerCount   int    `json:"TunerCount"`
	LineupURL    string `json:"LineupURL"`
}
type LineupChannel struct {
	Number     string `json:"GuideNumber"`
	Name       string `json:"GuideName"`
	URL        string `json:"URL"`
	GuideID    string `json:"GuideID"`
	Tags       any    `json:"Tags"`
	DRM        any    `json:"DRM"`
	Favorite   any    `json:"Favorite"`
	VideoCodec string `json:"VideoCodec"`
	AudioCodec string `json:"AudioCodec"`
}

func (c LineupChannel) Protected() bool {
	if c.DRM == true || c.DRM == float64(1) || c.DRM == "1" {
		return true
	}
	switch tags := c.Tags.(type) {
	case string:
		for _, tag := range strings.Split(tags, ",") {
			if strings.EqualFold(strings.TrimSpace(tag), "drm") {
				return true
			}
		}
	case []any:
		for _, tag := range tags {
			if s, ok := tag.(string); ok && strings.EqualFold(s, "drm") {
				return true
			}
		}
	}
	return false
}

type DeviceClient struct {
	base    *url.URL
	allowed map[string]bool
	HTTP    *http.Client
}

func NewDeviceClient(ctx context.Context, e Endpoint) (*DeviceClient, error) {
	base, err := ParseHTTPURL(e.URL)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{strings.ToLower(base.Hostname()): true}
	for _, host := range e.AllowedHosts {
		allowed[strings.ToLower(host)] = true
	}
	// A real tuner often advertises its IP after the admin configured its DNS name.
	// Accept only addresses the explicitly configured name actually resolves to.
	if _, err := netip.ParseAddr(base.Hostname()); err != nil {
		resolveCtx, cancel := context.WithTimeout(ctx, time.Second)
		ips, _ := net.DefaultResolver.LookupNetIP(resolveCtx, "ip", base.Hostname())
		cancel()
		for _, ip := range ips {
			allowed[ip.Unmap().String()] = true
		}
	}
	c := &DeviceClient{base: base, allowed: allowed}
	dialer := &net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return errors.New("invalid network address")
		}
		a, err := netip.ParseAddr(host)
		if err != nil {
			return errors.New("invalid network address")
		}
		a = a.Unmap()
		if a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsUnspecified() || a.String() == "255.255.255.255" {
			return errors.New("network address is not permitted")
		}
		return nil
	}}
	transport := &http.Transport{Proxy: nil, DialContext: dialer.DialContext, ResponseHeaderTimeout: 4 * time.Second, TLSHandshakeTimeout: 4 * time.Second, MaxIdleConns: 16, IdleConnTimeout: 60 * time.Second, ForceAttemptHTTP2: true}
	c.HTTP = &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return errors.New("too many redirects")
		}
		return c.Check(req.URL)
	}}
	return c, nil
}
func (c *DeviceClient) Close() { c.HTTP.CloseIdleConnections() }
func (c *DeviceClient) Check(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Fragment != "" {
		return errors.New("invalid device resource address")
	}
	host := strings.ToLower(u.Hostname())
	if !c.allowed[host] && !c.allowed[strings.ToLower(u.Host)] {
		return errors.New("device advertised an unapproved host; add it to the endpoint's allowed hosts")
	}
	return nil
}
func (c *DeviceClient) Resolve(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("invalid resource address")
	}
	base := *c.base
	base.Path = strings.TrimRight(base.Path, "/") + "/"
	u = base.ResolveReference(u)
	if err = c.Check(u); err != nil {
		return "", err
	}
	return u.String(), nil
}
func (c *DeviceClient) Get(ctx context.Context, raw string) (*http.Response, error) {
	return c.GetRange(ctx, raw, "")
}
func (c *DeviceClient) GetRange(ctx context.Context, raw, byteRange string) (*http.Response, error) {
	u, err := ParseHTTPURL(raw)
	if err != nil {
		return nil, errors.New("invalid resource address")
	}
	if err = c.Check(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("invalid request")
	}
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	r, err := c.HTTP.Do(req)
	if err != nil {
		return nil, errors.New("device request failed")
	}
	return r, nil
}
func (c *DeviceClient) JSON(ctx context.Context, raw string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	r, err := c.Get(ctx, raw)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return errors.New("device returned an unsuccessful HTTP status")
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return errors.New("device response is too large or incomplete")
	}
	if err = json.Unmarshal(data, out); err != nil {
		return errors.New("device returned invalid JSON")
	}
	return nil
}
func (c *DeviceClient) Discover(ctx context.Context) (Discovery, error) {
	var d Discovery
	raw, _ := c.Resolve("discover.json")
	err := c.JSON(ctx, raw, &d)
	return d, err
}
func (c *DeviceClient) Lineup(ctx context.Context, d Discovery) ([]LineupChannel, error) {
	raw := d.LineupURL
	if raw == "" {
		raw = "lineup.json"
	}
	resolved, err := c.Resolve(raw)
	if err != nil {
		return nil, err
	}
	var channels []LineupChannel
	if err = c.JSON(ctx, resolved, &channels); err != nil {
		return nil, err
	}
	if len(channels) > 20000 {
		return nil, errors.New("device lineup is too large")
	}
	return channels, nil
}

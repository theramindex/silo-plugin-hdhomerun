package hdhr

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

const MaxEndpoints = 32

var identityPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type Config struct {
	Endpoints []Endpoint `json:"endpoints"`
}
type Endpoint struct {
	ID               string                    `json:"id"`
	Name             string                    `json:"name"`
	URL              string                    `json:"url"`
	Enabled          *bool                     `json:"enabled,omitempty"`
	CapacityGroup    string                    `json:"capacity_group,omitempty"`
	SharedTunerLimit int                       `json:"shared_tuner_limit,omitempty"`
	TunerLimit       int                       `json:"tuner_limit,omitempty"`
	Priority         int                       `json:"priority,omitempty"`
	AllowedHosts     []string                  `json:"allowed_hosts,omitempty"`
	GuideMode        string                    `json:"guide_mode,omitempty"`
	GuideURL         string                    `json:"guide_url,omitempty"`
	GuideURLFile     string                    `json:"guide_url_file,omitempty"`
	Channels         map[string]ChannelMapping `json:"channels,omitempty"`
}
type ChannelMapping struct {
	StationID string `json:"station_id,omitempty"`
	GuideID   string `json:"guide_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Hidden    bool   `json:"hidden,omitempty"`
}

func (e Endpoint) IsEnabled() bool { return e.Enabled == nil || *e.Enabled }
func (c Config) Normalize() (Config, error) {
	if len(c.Endpoints) > MaxEndpoints {
		return Config{}, errors.New("at most 32 endpoints are supported")
	}
	// Own every slice/map: a caller cannot mutate an installed configuration.
	encoded, err := json.Marshal(c)
	if err != nil {
		return Config{}, errors.New("invalid configuration")
	}
	var out Config
	_ = json.Unmarshal(encoded, &out)
	if out.Endpoints == nil {
		out.Endpoints = []Endpoint{}
	}
	seen := map[string]bool{}
	for i := range out.Endpoints {
		e := &out.Endpoints[i]
		e.ID = strings.TrimSpace(e.ID)
		e.Name = strings.TrimSpace(e.Name)
		if !identityPattern.MatchString(e.ID) || seen[e.ID] {
			return Config{}, errors.New("endpoint IDs must be unique and contain only letters, digits, underscores or hyphens")
		}
		seen[e.ID] = true
		if e.Name == "" {
			e.Name = e.ID
		}
		if len(e.Name) > 120 {
			return Config{}, errors.New("endpoint name is too long")
		}
		u, err := ParseHTTPURL(e.URL)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return Config{}, errors.New("device address must be an HTTP(S) base URL without credentials, query or fragment")
		}
		e.URL = strings.TrimRight(u.String(), "/")
		if (e.CapacityGroup != "" && !identityPattern.MatchString(e.CapacityGroup)) || e.SharedTunerLimit < 0 || e.SharedTunerLimit > 64 || (e.SharedTunerLimit > 0 && e.CapacityGroup == "") {
			return Config{}, errors.New("shared tuner capacity needs a valid server group and a limit from 0 to 64")
		}
		if e.TunerLimit < 0 || e.TunerLimit > 64 {
			return Config{}, errors.New("tuner limit must be between 0 (automatic) and 64")
		}
		if e.Priority < -1000 || e.Priority > 1000 {
			return Config{}, errors.New("endpoint priority must be between -1000 and 1000")
		}
		if e.GuideMode == "" {
			if e.GuideURL != "" || e.GuideURLFile != "" {
				e.GuideMode = "xmltv"
			} else {
				e.GuideMode = "none"
			}
		}
		if e.GuideMode != "none" && e.GuideMode != "xmltv" && e.GuideMode != "silicondust" {
			return Config{}, errors.New("guide mode must be none, xmltv or silicondust")
		}
		if e.GuideMode == "xmltv" && e.GuideURL == "" && e.GuideURLFile == "" {
			return Config{}, errors.New("an XMLTV URL or URL file is required")
		}
		if e.GuideURL != "" {
			if _, err := ParseHTTPURL(e.GuideURL); err != nil {
				return Config{}, errors.New("invalid XMLTV URL")
			}
		}
		if e.GuideURL != "" && e.GuideURLFile != "" {
			return Config{}, errors.New("use an XMLTV URL or URL file, not both")
		}
		if len(e.AllowedHosts) > 32 {
			return Config{}, errors.New("too many additional stream hosts")
		}
		for j, host := range e.AllowedHosts {
			host = strings.TrimSpace(host)
			u, err := ParseHTTPURL("http://" + host)
			if err != nil || u.Host != host || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return Config{}, errors.New("additional stream hosts must be hostnames or host:port values")
			}
			e.AllowedHosts[j] = strings.ToLower(host)
		}
		if len(e.Channels) > 20000 {
			return Config{}, errors.New("too many channel mappings")
		}
		for number, m := range e.Channels {
			if strings.TrimSpace(number) == "" || len(number) > 64 || len(m.GuideID) > 256 || len(m.Name) > 200 {
				return Config{}, errors.New("invalid channel mapping")
			}
			if m.StationID != "" && !identityPattern.MatchString(m.StationID) {
				return Config{}, errors.New("station IDs must contain only letters, digits, underscores or hyphens")
			}
		}
	}
	return out, nil
}
func ParseHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || strings.ContainsAny(raw, "\r\n\x00") {
		return nil, errors.New("invalid HTTP URL")
	}
	if u.Port() != "" { // url.Parse validates named and out-of-range syntax only partially.
		port := 0
		for _, c := range u.Port() {
			if c < '0' || c > '9' {
				return nil, errors.New("invalid port")
			}
			port = port*10 + int(c-'0')
			if port > 65535 {
				return nil, errors.New("invalid port")
			}
		}
		if port == 0 {
			return nil, errors.New("invalid port")
		}
	}
	return u, nil
}

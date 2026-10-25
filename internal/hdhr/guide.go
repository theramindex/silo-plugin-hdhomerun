package hdhr

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Program struct {
	ID         string   `json:"id"`
	ChannelID  string   `json:"channelId"`
	Title      string   `json:"title"`
	Summary    string   `json:"summary,omitempty"`
	StartUnix  int64    `json:"startUnix"`
	EndUnix    int64    `json:"endUnix"`
	ImageURL   string   `json:"imageUrl,omitempty"`
	Categories []string `json:"categories,omitempty"`
	IsMovie    bool     `json:"isMovie"`
	IsSports   bool     `json:"isSports"`
	IsNews     bool     `json:"isNews"`
	IsKids     bool     `json:"isKids"`
	IsSeries   bool     `json:"isSeries"`
	IsLive     bool     `json:"isLive"`
}

func (p Program) ForChannel(id string) Program {
	p.ChannelID = id
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%s", id, p.StartUnix, p.EndUnix, p.Title)))
	p.ID = "hdhr-program:" + hex.EncodeToString(digest[:12])
	return p
}

type Guide struct {
	Channels map[string][]string `json:"channels"`
	Logos    map[string]string   `json:"logos"`
	Programs []Program           `json:"programs"`
}

func (g Guide) MatchChannel(ch LineupChannel) string {
	if _, ok := g.Channels[ch.GuideID]; ok && ch.GuideID != "" {
		return ch.GuideID
	}
	if _, ok := g.Channels[ch.Number]; ok {
		return ch.Number
	}
	found := ""
	for id, names := range g.Channels {
		match := false
		for _, name := range names {
			if strings.EqualFold(strings.TrimSpace(name), ch.Number) || strings.EqualFold(strings.TrimSpace(name), ch.Name) {
				match = true
				break
			}
		}
		if match {
			if found != "" && found != id {
				return ""
			}
			found = id
		}
	}
	return found
}
func fetchGuide(ctx context.Context, ep Endpoint, d Discovery) (Guide, time.Duration, error) {
	raw := ep.GuideURL
	delay := 6 * time.Hour
	if ep.GuideURLFile != "" {
		data, err := os.ReadFile(ep.GuideURLFile)
		if err != nil || len(data) > 8192 {
			return Guide{}, 0, errors.New("unable to read the XMLTV URL file")
		}
		raw = strings.TrimSpace(string(data))
	}
	if ep.GuideMode == "silicondust" {
		if d.DeviceAuth == "" {
			return Guide{}, 0, errors.New("device does not provide SiliconDust guide access; configure an XMLTV URL")
		}
		u, _ := url.Parse("https://api.hdhomerun.com/api/xmltv")
		q := u.Query()
		q.Set("DeviceAuth", d.DeviceAuth)
		u.RawQuery = q.Encode()
		raw = u.String()
		var jitter [2]byte
		_, _ = rand.Read(jitter[:])
		delay = 20*time.Hour + time.Duration(uint16(jitter[0])<<8|uint16(jitter[1]))*(8*time.Hour)/65536
	}
	u, err := ParseHTTPURL(raw)
	if err != nil {
		return Guide{}, 0, errors.New("invalid XMLTV URL")
	}
	base := *u
	base.RawQuery = ""
	base.User = nil
	base.Path = ""
	client, err := NewDeviceClient(ctx, Endpoint{URL: base.String()})
	if err != nil {
		return Guide{}, 0, errors.New("unable to reach the guide source")
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	r, err := client.Get(ctx, raw)
	if err != nil {
		return Guide{}, 0, errors.New("unable to download the XMLTV guide")
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return Guide{}, 0, errors.New("XMLTV source returned an unsuccessful HTTP status")
	}
	var reader io.Reader = r.Body
	// Go normally decompresses gzip automatically; also accept .gz servers that
	// send a compressed body without Content-Encoding.
	if !r.Uncompressed && (strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") || strings.HasSuffix(strings.ToLower(u.Path), ".gz")) {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return Guide{}, 0, errors.New("invalid compressed XMLTV guide")
		}
		defer gz.Close()
		reader = gz
	}
	guide, err := ParseGuide(reader, time.Now())
	if err != nil {
		return Guide{}, 0, err
	}
	return guide, delay, nil
}
func ParseGuide(reader io.Reader, now time.Time) (Guide, error) {
	const maxBytes = 32 << 20
	bounded := &io.LimitedReader{R: reader, N: maxBytes + 1}
	dec := xml.NewDecoder(bounded)
	g := Guide{Channels: map[string][]string{}, Logos: map[string]string{}, Programs: []Program{}}
	root := false
	closed := false
	for {
		token, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Guide{}, errors.New("invalid or oversized XMLTV guide")
		}
		if bounded.N <= 0 {
			return Guide{}, errors.New("XMLTV guide exceeds 32 MiB")
		}
		if end, ok := token.(xml.EndElement); ok && end.Name.Local == "tv" {
			closed = true
			continue
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if !root {
			if start.Name.Local != "tv" {
				return Guide{}, errors.New("XMLTV guide must start with a tv element")
			}
			root = true
			continue
		}
		if closed {
			return Guide{}, errors.New("invalid XMLTV document")
		}
		switch start.Name.Local {
		case "channel":
			var ch struct {
				ID    string   `xml:"id,attr"`
				Names []string `xml:"display-name"`
				Icon  struct {
					Src string `xml:"src,attr"`
				} `xml:"icon"`
			}
			if dec.DecodeElement(&ch, &start) != nil {
				return Guide{}, errors.New("invalid XMLTV channel")
			}
			if len(g.Channels) >= 30000 {
				return Guide{}, errors.New("too many XMLTV channels")
			}
			if ch.ID != "" {
				g.Channels[ch.ID] = ch.Names
				if _, err := ParseHTTPURL(ch.Icon.Src); err == nil {
					g.Logos[ch.ID] = ch.Icon.Src
				}
			}
		case "programme":
			var entry struct {
				Channel     string    `xml:"channel,attr"`
				Start       string    `xml:"start,attr"`
				Stop        string    `xml:"stop,attr"`
				Title       string    `xml:"title"`
				Description string    `xml:"desc"`
				Categories  []string  `xml:"category"`
				Episode     string    `xml:"episode-num"`
				Live        *struct{} `xml:"live"`
				Icon        struct {
					Src string `xml:"src,attr"`
				} `xml:"icon"`
			}
			if dec.DecodeElement(&entry, &start) != nil {
				return Guide{}, errors.New("invalid XMLTV programme")
			}
			from, err1 := guideTime(entry.Start)
			to, err2 := guideTime(entry.Stop)
			if err1 != nil || err2 != nil || !to.After(from) || entry.Channel == "" || entry.Title == "" || to.Before(now.Add(-24*time.Hour)) || from.After(now.Add(15*24*time.Hour)) {
				continue
			}
			if len(g.Programs) >= 100000 {
				return Guide{}, errors.New("too many XMLTV programmes")
			}
			p := Program{ChannelID: entry.Channel, Title: limitText(entry.Title, 512), Summary: limitText(entry.Description, 8192), StartUnix: from.Unix(), EndUnix: to.Unix(), Categories: entry.Categories, IsSeries: entry.Episode != "", IsLive: entry.Live != nil}
			if _, err := ParseHTTPURL(entry.Icon.Src); err == nil {
				p.ImageURL = entry.Icon.Src
			}
			for _, category := range p.Categories {
				switch strings.ToLower(category) {
				case "movie", "film":
					p.IsMovie = true
				case "sports", "sport", "football", "basketball", "baseball", "hockey", "soccer":
					p.IsSports = true
				case "news", "current affairs":
					p.IsNews = true
				case "children", "kids", "children's":
					p.IsKids = true
				case "series", "tv series":
					p.IsSeries = true
				}
			}

			switch strings.ToLower(strings.TrimSpace(p.Title)) {
			case "paid programming", "teleshopping", "off air", "off-air", "no event", "no events", "no information", "to be announced":
				p.IsMovie, p.IsSports, p.IsNews, p.IsKids, p.IsSeries, p.IsLive = false, false, false, false, false, false
			}
			g.Programs = append(g.Programs, p)
		default:
			if dec.Skip() != nil {
				return Guide{}, errors.New("invalid XMLTV element")
			}
		}
	}
	if !root || !closed || bounded.N <= 0 {
		return Guide{}, errors.New("invalid or oversized XMLTV guide")
	}
	return g, nil
}
func guideTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{"20060102150405 -0700", "200601021504 -0700", "20060102150405", "200601021504"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("invalid XMLTV time")
}
func limitText(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

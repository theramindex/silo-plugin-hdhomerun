package hdhr

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theramindex/silo-plugins/pkg/epg"
)

// Raw listings are scoped to their endpoint. Display-channel mappings and
// priorities remain in the lineup, so changing them never merges two devices'
// guide identities or requires a second persisted copy of the programmes.
type guideRecord struct {
	EndpointID string  `json:"endpointId"`
	Program    Program `json:"program"`
}

func guideChannelKey(endpoint, channel string) string { return endpoint + "\x00" + channel }
func guideRecordFields(record guideRecord) epg.Fields {
	p := record.Program
	return epg.Fields{ID: p.ID, ChannelID: guideChannelKey(record.EndpointID, p.ChannelID), Start: p.StartUnix, End: p.EndUnix}
}

// persistGuide commits the entire device/lineup metadata with a replacement
// guide, or updates metadata alone when no endpoint's guide changed. Callers
// publish devices and close superseded clients only after this succeeds.
func (e *Engine) persistGuide(ctx context.Context, states map[string]deviceState, updated int64, previousView *epg.View[guideRecord], signature string) (*epg.View[guideRecord], error) {
	replace := previousView == nil
	changed := map[string]bool{}
	for id, state := range states {
		if state.guideChanged || state.Guide.Programs != nil {
			changed[id] = true
			replace = true
		}
	}
	records := []guideRecord{}
	if replace && previousView != nil {
		previous, err := previousView.Query(ctx, epg.Query{Start: math.MinInt64, End: math.MaxInt64})
		if err != nil {
			return nil, err
		}
		for _, record := range previous {
			if _, keep := states[record.EndpointID]; keep && !changed[record.EndpointID] {
				records = append(records, record)
			}
		}
	}
	for id, state := range states {
		if changed[id] {
			state.ProgramCount = len(state.Guide.Programs)
			for _, p := range state.Guide.Programs {
				records = append(records, guideRecord{EndpointID: id, Program: p})
			}
		}
		state.guideChanged = false
		state.Guide.Programs = nil
		states[id] = state
	}
	metadata, err := json.Marshal(diskState{Signature: signature, Devices: states, UpdatedAtUnix: updated})
	if err != nil {
		return nil, err
	}
	if replace {
		return e.guideStore.Replace(ctx, metadata, records)
	}
	return previousView, e.guideStore.SaveMetadata(ctx, metadata, previousView)
}

func (e *Engine) Programs(ctx context.Context, ids []string, start, end int64) ([]Program, error) {
	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	for attempt := 0; attempt < 2; attempt++ {
		e.mu.Lock()
		view := e.guideView
		legacy := map[string][]Program{}
		if view == nil {
			for id, d := range e.devices {
				legacy[id] = d.Guide.Programs
			}
		}
		channels := e.channels // The lineup is immutable between rebuilds.
		e.mu.Unlock()
		selected := map[string]bool{}
		for _, id := range ids {
			selected[id] = true
		}
		type target struct {
			id       string
			priority int
		}
		targets := map[string][]target{}
		keys := []string{}
		for _, ch := range channels {
			if len(ids) > 0 && !selected[ch.ID] {
				continue
			}
			for priority, source := range ch.sources {
				if source.GuideID == "" {
					continue
				}
				key := guideChannelKey(source.EndpointID, source.GuideID)
				if _, exists := targets[key]; !exists {
					keys = append(keys, key)
				}
				targets[key] = append(targets[key], target{id: ch.ID, priority: priority})
			}
		}
		if len(keys) == 0 {
			return []Program{}, nil
		}
		var records []guideRecord
		var err error
		if view != nil {
			records, err = view.Query(ctx, epg.Query{ChannelIDs: keys, Start: start, End: end})
		} else {
			for endpoint, programs := range legacy {
				for _, p := range programs {
					if _, ok := targets[guideChannelKey(endpoint, p.ChannelID)]; !ok || p.StartUnix >= end || p.EndUnix <= start {
						continue
					}
					records = append(records, guideRecord{EndpointID: endpoint, Program: p})
				}
			}
		}
		if errors.Is(err, epg.ErrChanged) {
			runtime.Gosched()
			continue
		}
		if err != nil {
			return nil, err
		}
		out := []Program{}
		positions, priorities := map[string]int{}, map[string]int{}
		for _, record := range records {
			for _, target := range targets[guideChannelKey(record.EndpointID, record.Program.ChannelID)] {
				key := target.id + "\x00" + strconv.FormatInt(record.Program.StartUnix, 10)
				if pos, exists := positions[key]; exists {
					if target.priority < priorities[key] {
						out[pos] = record.Program.ForChannel(target.id)
						priorities[key] = target.priority
					}
					continue
				}
				positions[key], priorities[key] = len(out), target.priority
				out = append(out, record.Program.ForChannel(target.id))
			}
		}
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].ChannelID != out[j].ChannelID {
				return strings.Compare(out[i].ChannelID, out[j].ChannelID) < 0
			}
			return out[i].StartUnix < out[j].StartUnix
		})
		return out, nil
	}
	return nil, epg.ErrChanged
}

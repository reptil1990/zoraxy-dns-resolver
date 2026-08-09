package main

import (
	"sort"
	"sync"
	"time"
)

type stats struct {
	mu         sync.RWMutex
	startTime  time.Time
	total      int64
	local      int64
	cached     int64
	forwarded  int64
	errors     int64
	topClients map[string]int64
	queryTypes map[string]int64
}

func newStats(start time.Time) *stats {
	return &stats{
		startTime:  start,
		topClients: make(map[string]int64),
		queryTypes: make(map[string]int64),
	}
}

func (s *stats) record(client, qtype string) {
	s.mu.Lock()
	s.total++
	if client != "" {
		s.topClients[client]++
	}
	if qtype != "" {
		s.queryTypes[qtype]++
	}
	s.mu.Unlock()
}

func (s *stats) markLocal()     { s.mu.Lock(); s.local++; s.mu.Unlock() }
func (s *stats) markCached()    { s.mu.Lock(); s.cached++; s.mu.Unlock() }
func (s *stats) markForwarded() { s.mu.Lock(); s.forwarded++; s.mu.Unlock() }
func (s *stats) markError()     { s.mu.Lock(); s.errors++; s.mu.Unlock() }

type topEntry struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type statsSnapshot struct {
	UptimeSeconds int64      `json:"uptime_seconds"`
	Total         int64      `json:"total"`
	Local         int64      `json:"local"`
	Cached        int64      `json:"cached"`
	Forwarded     int64      `json:"forwarded"`
	Errors        int64      `json:"errors"`
	TopClients    []topEntry `json:"top_clients"`
	QueryTypes    []topEntry `json:"query_types"`
}

func topN(m map[string]int64, n int) []topEntry {
	out := make([]topEntry, 0, len(m))
	for k, v := range m {
		out = append(out, topEntry{Name: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func (s *stats) snapshot(now time.Time) statsSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return statsSnapshot{
		UptimeSeconds: int64(now.Sub(s.startTime).Seconds()),
		Total:         s.total,
		Local:         s.local,
		Cached:        s.cached,
		Forwarded:     s.forwarded,
		Errors:        s.errors,
		TopClients:    topN(s.topClients, 10),
		QueryTypes:    topN(s.queryTypes, 10),
	}
}

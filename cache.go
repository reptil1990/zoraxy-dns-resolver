package main

import (
	"sync"
	"time"

	"github.com/miekg/dns"
)

type cacheEntry struct {
	msg     *dns.Msg
	expires time.Time
	ttl     uint32
}

type dnsCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
}

func newCache() *dnsCache {
	c := &dnsCache{entries: make(map[string]cacheEntry)}
	go c.cleanupLoop()
	return c
}

func cacheKey(q dns.Question) string {
	return q.Name + "|" + dns.TypeToString[q.Qtype]
}

// minTTL returns the smallest TTL across all answer records, or 0 if there
// are none.
func minTTL(msg *dns.Msg) uint32 {
	if len(msg.Answer) == 0 {
		return 0
	}
	ttl := msg.Answer[0].Header().Ttl
	for _, rr := range msg.Answer[1:] {
		if t := rr.Header().Ttl; t < ttl {
			ttl = t
		}
	}
	return ttl
}

// get returns a copy of the cached reply with TTLs decremented by the elapsed
// time, or nil on a miss.
func (c *dnsCache) get(q dns.Question, now time.Time) *dns.Msg {
	c.mu.RLock()
	entry, ok := c.entries[cacheKey(q)]
	c.mu.RUnlock()
	if !ok || now.After(entry.expires) {
		return nil
	}

	remaining := uint32(entry.expires.Sub(now).Seconds())
	reply := entry.msg.Copy()
	for _, rr := range reply.Answer {
		rr.Header().Ttl = remaining
	}
	return reply
}

// set stores a reply. Responses that are truncated, carry an error rcode or
// have a zero TTL are not cached.
func (c *dnsCache) set(q dns.Question, msg *dns.Msg, now time.Time) {
	if msg.Truncated || msg.Rcode != dns.RcodeSuccess {
		return
	}
	ttl := minTTL(msg)
	if ttl == 0 {
		return
	}
	c.mu.Lock()
	c.entries[cacheKey(q)] = cacheEntry{
		msg:     msg.Copy(),
		expires: now.Add(time.Duration(ttl) * time.Second),
		ttl:     ttl,
	}
	c.mu.Unlock()
}

func (c *dnsCache) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	for now := range ticker.C {
		c.mu.Lock()
		for k, e := range c.entries {
			if now.After(e.expires) {
				delete(c.entries, k)
			}
		}
		c.mu.Unlock()
	}
}

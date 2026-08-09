package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// recordSet is an immutable snapshot of the internal DNS records.
type recordSet struct {
	exact    map[string]net.IP
	wildcard []wildcardRecord // matched as suffix of the query name
}

type wildcardRecord struct {
	suffix string // e.g. "example.com" for a "*.example.com" rule
	ip     net.IP
}

func (rs *recordSet) lookup(name string) (net.IP, bool) {
	if ip, ok := rs.exact[name]; ok {
		return ip, true
	}
	for _, w := range rs.wildcard {
		if strings.HasSuffix(name, "."+w.suffix) {
			return w.ip, true
		}
	}
	return nil, false
}

// records manages the internal record set behind an atomic pointer so the DNS
// handler reads it lock-free while a rebuild swaps in a new snapshot.
type records struct {
	set    atomic.Pointer[recordSet]
	cfg    *configStore
	zoraxy *zoraxyClient
	mu     sync.Mutex // serializes rebuilds
	count  atomic.Int64
}

func newRecords(cfg *configStore, zoraxy *zoraxyClient) *records {
	r := &records{cfg: cfg, zoraxy: zoraxy}
	r.set.Store(&recordSet{exact: map[string]net.IP{}})
	return r
}

func (r *records) lookup(name string) (net.IP, bool) { return r.set.Load().lookup(name) }
func (r *records) size() int64                        { return r.count.Load() }

// rebuild recomputes the record set from static entries plus, if enabled, the
// hostnames auto-synced from Zoraxy's reverse proxy rules.
func (r *records) rebuild() {
	r.mu.Lock()
	defer r.mu.Unlock()

	cfg := r.cfg.get()
	rs := &recordSet{exact: map[string]net.IP{}}

	add := func(host, ipStr string) {
		ip := net.ParseIP(strings.TrimSpace(ipStr))
		if ip == nil {
			return
		}
		host = normalizeDomain(host)
		if host == "" {
			return
		}
		if strings.HasPrefix(host, "*.") {
			rs.wildcard = append(rs.wildcard, wildcardRecord{suffix: host[2:], ip: ip})
			return
		}
		rs.exact[host] = ip
	}

	for _, rec := range cfg.StaticRecords {
		add(rec.Host, rec.IP)
	}

	if cfg.AutoSync && cfg.ZoraxyLANIP != "" && r.zoraxy != nil {
		hosts, err := r.zoraxy.proxyHosts()
		if err != nil {
			fmt.Fprintf(logOut, "auto-sync: %v\n", err)
		} else {
			for _, h := range hosts {
				add(h, cfg.ZoraxyLANIP)
			}
		}
	}

	r.set.Store(rs)
	r.count.Store(int64(len(rs.exact) + len(rs.wildcard)))
	fmt.Fprintf(logOut, "records rebuilt: %d entries\n", len(rs.exact)+len(rs.wildcard))
}

// startSync rebuilds immediately and then re-syncs on an interval so that new
// Zoraxy proxy rules are picked up without a manual save.
func (r *records) startSync() {
	go func() {
		r.rebuild()
		ticker := time.NewTicker(60 * time.Second)
		for range ticker.C {
			if r.cfg.get().AutoSync {
				r.rebuild()
			}
		}
	}()
}

// zoraxyClient calls the Zoraxy API through the plugin API proxy.
type zoraxyClient struct {
	port   int
	apiKey string
	client *http.Client
}

func newZoraxyClient(port int, apiKey string) *zoraxyClient {
	return &zoraxyClient{
		port:   port,
		apiKey: apiKey,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

type proxyEndpoint struct {
	RootOrMatchingDomain string   `json:"RootOrMatchingDomain"`
	MatchingDomainAlias  []string `json:"MatchingDomainAlias"`
	Disabled             bool     `json:"Disabled"`
}

// proxyHosts returns the hostnames of all enabled HTTP reverse proxy rules.
func (z *zoraxyClient) proxyHosts() ([]string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/plugin/api/proxy/list?type=host", z.port)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+z.apiKey)

	resp, err := z.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var endpoints []proxyEndpoint
	if err := json.Unmarshal(body, &endpoints); err != nil {
		return nil, err
	}

	var hosts []string
	for _, ep := range endpoints {
		if ep.Disabled {
			continue
		}
		if ep.RootOrMatchingDomain != "" {
			hosts = append(hosts, ep.RootOrMatchingDomain)
		}
		hosts = append(hosts, ep.MatchingDomainAlias...)
	}
	return hosts, nil
}

func normalizeDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimSuffix(d, ".")
	return d
}

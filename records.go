package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
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

// syncStatus is the last auto-sync result, surfaced in the UI for diagnostics.
type syncStatus struct {
	Enabled         bool   `json:"enabled"`
	LastRunUnix     int64  `json:"last_run_unix"`
	SyncedHosts     int    `json:"synced_hosts"`     // enabled hosts turned into records
	DiscoveredHosts int    `json:"discovered_hosts"` // total hosts found in Zoraxy
	LanIP           string `json:"lan_ip"`           // address auto-synced hosts resolve to
	Error           string `json:"error"`
}

// syncedHost is a proxy host discovered via auto-sync, with its on/off state.
type syncedHost struct {
	Host    string `json:"host"`
	Enabled bool   `json:"enabled"`
}

// detectLocalIP returns the host's primary LAN IPv4 by inspecting which local
// address the OS would route outbound traffic from. No packet is sent. The
// plugin always runs on the Zoraxy host, so this is the address LAN clients
// should reach Zoraxy at. Falls back to loopback if detection fails.
func detectLocalIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// records manages the internal record set behind an atomic pointer so the DNS
// handler reads it lock-free while a rebuild swaps in a new snapshot.
type records struct {
	set    atomic.Pointer[recordSet]
	status atomic.Pointer[syncStatus]
	hosts  atomic.Pointer[[]syncedHost]
	cfg    *configStore
	zoraxy *zoraxyClient
	mu     sync.Mutex // serializes rebuilds
	count  atomic.Int64
}

func newRecords(cfg *configStore, zoraxy *zoraxyClient) *records {
	r := &records{cfg: cfg, zoraxy: zoraxy}
	r.set.Store(&recordSet{exact: map[string]net.IP{}})
	r.status.Store(&syncStatus{})
	r.hosts.Store(&[]syncedHost{})
	return r
}

func (r *records) lookup(name string) (net.IP, bool) { return r.set.Load().lookup(name) }
func (r *records) size() int64                        { return r.count.Load() }
func (r *records) syncStatus() *syncStatus            { return r.status.Load() }
func (r *records) syncedHosts() []syncedHost          { return *r.hosts.Load() }

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

	lanIP := strings.TrimSpace(cfg.ZoraxyLANIP)
	if lanIP == "" {
		lanIP = detectLocalIP()
	}
	status := &syncStatus{Enabled: cfg.AutoSync, LastRunUnix: time.Now().Unix(), LanIP: lanIP}
	discovered := []syncedHost{}
	if cfg.AutoSync {
		switch {
		case r.zoraxy == nil || r.zoraxy.apiKey == "":
			status.Error = "no Zoraxy API key — grant this plugin API access in Zoraxy"
		default:
			hosts, err := r.zoraxy.proxyHosts()
			if err != nil {
				status.Error = err.Error()
				fmt.Fprintf(logOut, "auto-sync: %v\n", err)
			} else {
				disabled := make(map[string]struct{}, len(cfg.AutoSyncDisabled))
				for _, d := range cfg.AutoSyncDisabled {
					disabled[normalizeDomain(d)] = struct{}{}
				}
				seen := make(map[string]struct{}, len(hosts))
				for _, h := range hosts {
					hn := normalizeDomain(h)
					if hn == "" {
						continue
					}
					if _, dup := seen[hn]; dup {
						continue
					}
					seen[hn] = struct{}{}
					_, off := disabled[hn]
					enabled := !off
					discovered = append(discovered, syncedHost{Host: hn, Enabled: enabled})
					if enabled {
						add(hn, lanIP)
					}
				}
				sort.Slice(discovered, func(i, j int) bool { return discovered[i].Host < discovered[j].Host })
				status.DiscoveredHosts = len(discovered)
				for _, d := range discovered {
					if d.Enabled {
						status.SyncedHosts++
					}
				}
			}
		}
	}

	r.set.Store(rs)
	r.status.Store(status)
	r.hosts.Store(&discovered)
	r.count.Store(int64(len(rs.exact) + len(rs.wildcard)))
	fmt.Fprintf(logOut, "records rebuilt: %d entries (auto-sync %d/%d hosts enabled)\n",
		len(rs.exact)+len(rs.wildcard), status.SyncedHosts, status.DiscoveredHosts)
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
// Zoraxy's /api/proxy/list reads the "type" parameter via PostPara, so it is
// sent as a form body (POST); it is also kept in the query string as a fallback.
func (z *zoraxyClient) proxyHosts() ([]string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/plugin/api/proxy/list?type=host", z.port)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader("type=host"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, snippet(body))
	}

	// Zoraxy returns an error object ({"error":"..."}) rather than an array on
	// failure; catch that instead of surfacing a raw unmarshal error.
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, fmt.Errorf("unexpected response: %s", snippet(trimmed))
	}

	var endpoints []proxyEndpoint
	if err := json.Unmarshal(trimmed, &endpoints); err != nil {
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

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func normalizeDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimSuffix(d, ".")
	return d
}

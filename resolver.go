package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const localRecordTTL = 60

type resolver struct {
	cfg   *configStore
	cache *dnsCache
	recs  *records
	stats *stats
}

// ServeDNS handles a single query: local record → cache → forward to upstream.
func (r *resolver) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	if len(req.Question) == 0 {
		m := new(dns.Msg)
		m.SetReply(req)
		m.Rcode = dns.RcodeFormatError
		_ = w.WriteMsg(m)
		return
	}

	q := req.Question[0]
	cfg := r.cfg.get()
	r.stats.record(clientIP(w.RemoteAddr()), dns.TypeToString[q.Qtype])

	name := normalizeDomain(q.Name)
	if ip, ok := r.recs.lookup(name); ok {
		r.stats.markLocal()
		_ = w.WriteMsg(localAnswer(req, q, ip))
		return
	}

	now := time.Now()
	if cached := r.cache.get(q, now); cached != nil {
		cached.Id = req.Id
		r.stats.markCached()
		_ = w.WriteMsg(cached)
		return
	}

	resp, err := r.forward(req, cfg, w.RemoteAddr().Network())
	if err != nil || resp == nil {
		r.stats.markError()
		m := new(dns.Msg)
		m.SetReply(req)
		m.Rcode = dns.RcodeServerFailure
		_ = w.WriteMsg(m)
		return
	}

	r.cache.set(q, resp, now)
	r.stats.markForwarded()
	resp.Id = req.Id
	_ = w.WriteMsg(resp)
}

// localAnswer builds an authoritative reply for a known internal host. If the
// query type does not match the record's address family it returns an empty
// NOERROR (NODATA) so the name is not leaked to the upstream resolver.
func localAnswer(req *dns.Msg, q dns.Question, ip net.IP) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true

	ip4 := ip.To4()
	switch q.Qtype {
	case dns.TypeA:
		if ip4 != nil {
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: localRecordTTL},
				A:   ip4,
			})
		}
	case dns.TypeAAAA:
		if ip4 == nil {
			m.Answer = append(m.Answer, &dns.AAAA{
				Hdr:  dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: localRecordTTL},
				AAAA: ip,
			})
		}
	}
	return m
}

func (r *resolver) forward(req *dns.Msg, cfg Config, network string) (*dns.Msg, error) {
	if network != "tcp" {
		network = "udp"
	}
	client := &dns.Client{Net: network, Timeout: 5 * time.Second}

	var lastErr error
	for _, up := range cfg.Upstreams {
		up = ensureDNSPort(up)
		if up == "" {
			continue
		}
		resp, _, err := client.Exchange(req, up)
		if err == nil && resp != nil {
			return resp, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no upstream configured")
	}
	return nil, lastErr
}

func clientIP(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

func ensureDNSPort(up string) string {
	up = strings.TrimSpace(up)
	if up == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(up); err != nil {
		return net.JoinHostPort(up, "53")
	}
	return up
}

// dnsManager owns the UDP and TCP listeners and can rebind them on a port change.
type dnsManager struct {
	mu      sync.Mutex
	handler dns.Handler
	port    int
	udp     *dns.Server
	tcp     *dns.Server
}

func (m *dnsManager) apply(port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.udp != nil && m.port == port {
		return nil
	}
	if m.udp != nil {
		_ = m.udp.Shutdown()
		m.udp = nil
	}
	if m.tcp != nil {
		_ = m.tcp.Shutdown()
		m.tcp = nil
	}

	addr := ":" + strconv.Itoa(port)
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		_ = pc.Close()
		return err
	}

	udp := &dns.Server{PacketConn: pc, Handler: m.handler}
	tcp := &dns.Server{Listener: l, Handler: m.handler}
	go func() {
		if e := udp.ActivateAndServe(); e != nil {
			fmt.Fprintln(logOut, "dns udp:", e)
		}
	}()
	go func() {
		if e := tcp.ActivateAndServe(); e != nil {
			fmt.Fprintln(logOut, "dns tcp:", e)
		}
	}()

	m.udp, m.tcp, m.port = udp, tcp, port
	fmt.Fprintf(logOut, "DNS server listening on %s (udp/tcp)\n", addr)
	return nil
}

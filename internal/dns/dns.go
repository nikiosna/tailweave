// Package dns is tailweave's replacement for MagicDNS: an authoritative server for the
// tailnet domains that answers with the IP each device is routed to on this machine.
// Like MagicDNS, it also answers 4via6 names such as 10-1-2-3-via-7.<domain> with the
// IPv6 address of 10.1.2.3 at site 7, as long as a 4via6 route of the domain's tailnet
// that holds it is routed.
package dns

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Nikiosna/tailweave/internal/weave"
	"github.com/miekg/dns"
)

// TTL is short because mappings can change at any time.
const TTL = 10

type records struct {
	names   map[string]netip.Addr     // lowercase FQDN with trailing dot
	domains []string                  // with trailing dot
	via     map[string][]netip.Prefix // zone with trailing dot -> routed 4via6 prefixes
}

// Server answers queries for the tailnet domains. The zero value answers nothing;
// call Update to load records.
type Server struct {
	rec     atomic.Pointer[records]
	servers []*dns.Server
}

// Update replaces all records atomically. names map lowercase FQDNs with trailing dot
// to addresses; domains are the zones the server is authoritative for; via are the
// routed 4via6 prefixes per domain.
func (s *Server) Update(names map[string]netip.Addr, domains []string, via map[string][]netip.Prefix) {
	r := &records{names: names, via: map[string][]netip.Prefix{}}
	for _, d := range domains {
		r.domains = append(r.domains, dns.Fqdn(strings.ToLower(d)))
	}
	for d, ps := range via {
		r.via[dns.Fqdn(strings.ToLower(d))] = ps
	}
	s.rec.Store(r)
}

// Start listens on addr (host:port) over UDP and TCP and serves in the background.
func (s *Server) Start(addr string) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		pc.Close()
		return err
	}
	s.servers = []*dns.Server{
		{PacketConn: pc, Handler: s},
		{Listener: ln, Handler: s},
	}
	for _, srv := range s.servers {
		go func() { _ = srv.ActivateAndServe() }()
	}
	return nil
}

// Close stops the server.
func (s *Server) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, srv := range s.servers {
		_ = srv.ShutdownContext(ctx)
	}
}

// ServeDNS implements dns.Handler.
func (s *Server) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true
	r := s.rec.Load()
	if r == nil || len(req.Question) != 1 {
		m.Rcode = dns.RcodeRefused
		_ = w.WriteMsg(m)
		return
	}
	q := req.Question[0]
	name := strings.ToLower(q.Name)
	if ip, ok := r.names[name]; ok {
		if q.Qtype == dns.TypeA || q.Qtype == dns.TypeANY {
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: TTL},
				A:   ip.AsSlice(),
			})
		}
		// Other types: the name exists but has no such record (NODATA).
		_ = w.WriteMsg(m)
		return
	}
	if ip, ok := r.lookupVia(name); ok {
		if q.Qtype == dns.TypeAAAA || q.Qtype == dns.TypeANY {
			m.Answer = append(m.Answer, &dns.AAAA{
				Hdr:  dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: TTL},
				AAAA: ip.AsSlice(),
			})
		}
		_ = w.WriteMsg(m)
		return
	}
	switch zone := r.zone(name); {
	case zone == "":
		m.Rcode = dns.RcodeRefused
	case zone != name:
		m.Rcode = dns.RcodeNameError
	} // the zone apex itself exists without records
	_ = w.WriteMsg(m)
}

// lookupVia resolves a 4via6 name, <IPv4 with dashes>-via-<site>.<zone>, to its address
// if a routed 4via6 prefix of the zone holds it.
func (r *records) lookupVia(name string) (netip.Addr, bool) {
	label, zone, ok := strings.Cut(name, ".")
	if !ok || len(r.via[zone]) == 0 {
		return netip.Addr{}, false
	}
	ip, ok := viaAddr(label)
	if !ok {
		return netip.Addr{}, false
	}
	for _, p := range r.via[zone] {
		if p.Contains(ip) {
			return ip, true
		}
	}
	return netip.Addr{}, false
}

// viaAddr parses a 4via6 label like Tailscale does: 10-1-2-3-via-7, the site ID also in
// hex (0x7).
func viaAddr(label string) (netip.Addr, bool) {
	v4s, site, ok := strings.Cut(label, "-via-")
	if !ok {
		return netip.Addr{}, false
	}
	v4, err := netip.ParseAddr(strings.ReplaceAll(v4s, "-", "."))
	if err != nil || !v4.Is4() {
		return netip.Addr{}, false
	}
	id, err := strconv.ParseUint(site, 0, 32)
	if err != nil {
		return netip.Addr{}, false
	}
	b := weave.ViaRange.Addr().As16()
	binary.BigEndian.PutUint32(b[8:], uint32(id))
	a4 := v4.As4()
	copy(b[12:], a4[:])
	return netip.AddrFrom16(b), true
}

func (r *records) zone(name string) string {
	for _, d := range r.domains {
		if dns.IsSubDomain(d, name) {
			return d
		}
	}
	return ""
}

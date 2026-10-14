// Package dns is tailweave's replacement for MagicDNS: an authoritative server for the
// tailnet domains that answers with the IP each device is routed to on this machine.
package dns

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

// TTL is short because mappings can change at any time.
const TTL = 10

type records struct {
	names   map[string]netip.Addr // lowercase FQDN with trailing dot
	domains []string              // with trailing dot
}

// Server answers queries for the tailnet domains. The zero value answers nothing;
// call Update to load records.
type Server struct {
	rec     atomic.Pointer[records]
	servers []*dns.Server
}

// Update replaces all records atomically. names map lowercase FQDNs with trailing dot
// to addresses; domains are the zones the server is authoritative for.
func (s *Server) Update(names map[string]netip.Addr, domains []string) {
	r := &records{names: names}
	for _, d := range domains {
		r.domains = append(r.domains, dns.Fqdn(strings.ToLower(d)))
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
	switch zone := r.zone(name); {
	case zone == "":
		m.Rcode = dns.RcodeRefused
	case zone != name:
		m.Rcode = dns.RcodeNameError
	} // the zone apex itself exists without records
	_ = w.WriteMsg(m)
}

func (r *records) zone(name string) string {
	for _, d := range r.domains {
		if dns.IsSubDomain(d, name) {
			return d
		}
	}
	return ""
}

package dns

import (
	"net"
	"net/netip"
	"testing"

	"github.com/miekg/dns"
)

func start(t *testing.T) (*Server, string) {
	t.Helper()
	s := &Server{}
	s.Update(map[string]netip.Addr{
		"peer-a1.a.lab.": netip.MustParseAddr("100.64.0.1"),
		"peer-b1.b.lab.": netip.MustParseAddr("198.18.0.1"),
	}, []string{"a.lab", "b.lab"}, map[string][]netip.Prefix{
		"a.lab": {netip.MustParsePrefix("fd7a:115c:a1e0:b1a:0:7:a63:0/120")},
	})
	// Grab a free port, then let the server listen on it.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	pc.Close()
	if err := s.Start(addr); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, addr
}

func query(t *testing.T, addr, net, name string, qtype uint16) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(name, qtype)
	c := &dns.Client{Net: net}
	var r *dns.Msg
	var err error
	for range 20 { // the server goroutines may still be starting
		if r, _, err = c.Exchange(m, addr); err == nil {
			return r
		}
	}
	t.Fatalf("query %s: %v", name, err)
	return nil
}

func TestServer(t *testing.T) {
	s, addr := start(t)

	for _, tc := range []struct {
		name  string
		qtype uint16
		rcode int
		a     string
	}{
		{"peer-a1.a.lab.", dns.TypeA, dns.RcodeSuccess, "100.64.0.1"},
		{"PEER-B1.b.lab.", dns.TypeA, dns.RcodeSuccess, "198.18.0.1"},
		{"peer-b1.b.lab.", dns.TypeAAAA, dns.RcodeSuccess, ""},
		{"nope.b.lab.", dns.TypeA, dns.RcodeNameError, ""},
		{"b.lab.", dns.TypeA, dns.RcodeSuccess, ""},
		{"example.com.", dns.TypeA, dns.RcodeRefused, ""},
		{"10-99-0-1-via-7.a.lab.", dns.TypeAAAA, dns.RcodeSuccess, "fd7a:115c:a1e0:b1a:0:7:a63:1"},
		{"10-99-0-1-via-0x7.A.lab.", dns.TypeAAAA, dns.RcodeSuccess, "fd7a:115c:a1e0:b1a:0:7:a63:1"},
		{"10-99-0-1-via-7.a.lab.", dns.TypeA, dns.RcodeSuccess, ""},
		{"10-99-0-1-via-8.a.lab.", dns.TypeAAAA, dns.RcodeNameError, ""}, // site not routed
		{"10-98-0-1-via-7.a.lab.", dns.TypeAAAA, dns.RcodeNameError, ""}, // outside the prefix
		{"10-99-0-1-via-7.b.lab.", dns.TypeAAAA, dns.RcodeNameError, ""}, // other tailnet
		{"10-99-0-1-via-x.a.lab.", dns.TypeAAAA, dns.RcodeNameError, ""},
	} {
		for _, n := range []string{"udp", "tcp"} {
			r := query(t, addr, n, tc.name, tc.qtype)
			if r.Rcode != tc.rcode {
				t.Errorf("%s %s %s: rcode %s, want %s", n, tc.name, dns.TypeToString[tc.qtype],
					dns.RcodeToString[r.Rcode], dns.RcodeToString[tc.rcode])
				continue
			}
			var got string
			if len(r.Answer) == 1 {
				switch rr := r.Answer[0].(type) {
				case *dns.A:
					got = rr.A.String()
				case *dns.AAAA:
					got = rr.AAAA.String()
				}
			}
			if got != tc.a || len(r.Answer) > 1 {
				t.Errorf("%s %s: answer %v, want %q", n, tc.name, r.Answer, tc.a)
			}
		}
	}

	// Update swaps the records.
	s.Update(map[string]netip.Addr{"peer-b1.b.lab.": netip.MustParseAddr("10.77.0.1")}, []string{"b.lab"}, nil)
	if r := query(t, addr, "udp", "peer-b1.b.lab.", dns.TypeA); len(r.Answer) != 1 || r.Answer[0].(*dns.A).A.String() != "10.77.0.1" {
		t.Errorf("after update: %v", r.Answer)
	}
	if r := query(t, addr, "udp", "peer-a1.a.lab.", dns.TypeA); r.Rcode != dns.RcodeRefused {
		t.Errorf("removed zone: rcode %s", dns.RcodeToString[r.Rcode])
	}
}

package preflight

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

var claims = Claims{
	Ranges: []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("10.201.0.0/16"),
	},
	DNS: netip.MustParseAddr("100.100.100.100"),
	Rules: []Rule{
		{Pref: 5180, Iif: "lo", Table: "5180"},
		{Pref: 5181, Table: "main"},
		{Pref: 5182, Iif: "lo", Table: "main"},
		{Pref: 5183, Iif: "lo", Table: "5181"},
	},
	Tables:        []string{"5180", "5181"},
	OwnInterfaces: []string{"twh", "tailweave0"},
	OwnAddr:       netip.MustParseAddr("10.201.0.1"),
}

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

// lan is an ordinary machine on a home network.
var lan = Snapshot{
	Routes: []Route{
		{Prefix: pfx("0.0.0.0/0"), Dev: "wlan0", Table: "main", Type: "unicast"},
		{Prefix: pfx("192.168.1.0/24"), Dev: "wlan0", Table: "main", Type: "unicast"},
		{Prefix: pfx("127.0.0.0/8"), Dev: "lo", Table: "local", Type: "local"},
	},
	Rules: []Rule{
		{Pref: 0, Table: "local"},
		{Pref: 32766, Table: "main"},
		{Pref: 32767, Table: "default"},
	},
	Interfaces: []Interface{
		{Name: "lo", Addrs: []netip.Prefix{pfx("127.0.0.1/8")}},
		{Name: "wlan0", Addrs: []netip.Prefix{pfx("192.168.1.20/24")}},
	},
	Processes: []string{"systemd", "sshd", "bash"},
}

func with(s Snapshot, f func(*Snapshot)) Snapshot {
	s.Routes = slices.Clone(s.Routes)
	s.Rules = slices.Clone(s.Rules)
	s.Interfaces = slices.Clone(s.Interfaces)
	s.Processes = slices.Clone(s.Processes)
	f(&s)
	return s
}

func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    Snapshot
		want []string // substrings of the error; none: no conflict
	}{
		{name: "clean", s: lan},
		{
			name: "tailscaled on Linux",
			s: with(lan, func(s *Snapshot) {
				s.Interfaces = append(s.Interfaces, Interface{Name: "tailscale0", Addrs: []netip.Prefix{pfx("100.64.0.5/32")}})
				s.Routes = append(s.Routes,
					Route{Prefix: pfx("100.64.0.1/32"), Dev: "tailscale0", Table: "52", Type: "unicast"},
					Route{Prefix: pfx("100.64.0.2/32"), Dev: "tailscale0", Table: "52", Type: "unicast"},
					Route{Prefix: pfx("100.100.100.100/32"), Dev: "tailscale0", Table: "52", Type: "unicast"},
					Route{Prefix: pfx("127.0.0.0/8"), Table: "52", Type: "throw"},
					Route{Prefix: pfx("100.64.0.5/32"), Dev: "tailscale0", Table: "local", Type: "local"},
				)
				s.Rules = append(s.Rules,
					Rule{Pref: 5210, Table: "main"}, Rule{Pref: 5230, Table: "default"},
					Rule{Pref: 5250, Table: "unreachable"}, Rule{Pref: 5270, Table: "52"})
				s.Processes = append(s.Processes, "tailscaled")
			}),
			want: []string{"address 100.64.0.5/32 on tailscale0 overlaps 100.64.0.0/10",
				"route 100.64.0.1/32 via tailscale0 (table 52) overlaps 100.64.0.0/10", "Tailscale is running"},
		},
		{
			name: "Tailscale.app on macOS",
			s: Snapshot{
				Routes: []Route{
					{Prefix: pfx("0.0.0.0/0"), Dev: "en0"},
					{Prefix: pfx("0.0.0.0/0"), Dev: "utun4"},
					{Prefix: pfx("100.64.0.0/10"), Dev: "utun4"},
					{Prefix: pfx("100.100.100.100/32"), Dev: "utun4"},
					{Prefix: pfx("192.168.1.0/24"), Dev: "en0"},
				},
				Interfaces: []Interface{
					{Name: "lo0", Addrs: []netip.Prefix{pfx("127.0.0.1/8")}},
					{Name: "en0", Addrs: []netip.Prefix{pfx("192.168.1.20/24")}},
					{Name: "utun4", Addrs: []netip.Prefix{pfx("100.101.102.103/32")}},
				},
				Processes: []string{"launchd", "Tailscale", "io.tailscale.ipn"},
			},
			want: []string{"100.64.0.0/10 is already routed via utun4", "Tailscale is running"},
		},
		{
			name: "WireGuard full tunnel",
			s: with(lan, func(s *Snapshot) {
				s.Interfaces = append(s.Interfaces, Interface{Name: "wg0", Addrs: []netip.Prefix{pfx("10.8.0.2/24")}})
				s.Routes = append(s.Routes,
					Route{Prefix: pfx("0.0.0.0/1"), Dev: "wg0", Table: "main", Type: "unicast"},
					Route{Prefix: pfx("128.0.0.0/1"), Dev: "wg0", Table: "main", Type: "unicast"},
					Route{Prefix: pfx("0.0.0.0/0"), Dev: "wg0", Table: "51820", Type: "unicast"},
				)
				s.Rules = append(s.Rules, Rule{Pref: 32764, Table: "main"}, Rule{Pref: 32765, Table: "51820"})
			}),
		},
		{
			name: "CGNAT address on the WLAN",
			s: with(lan, func(s *Snapshot) {
				s.Interfaces[1].Addrs = []netip.Prefix{pfx("100.72.3.4/16")}
				s.Routes[1] = Route{Prefix: pfx("100.72.0.0/16"), Dev: "wlan0", Table: "main", Type: "unicast"}
			}),
			want: []string{"address 100.72.3.4/16 on wlan0 overlaps 100.64.0.0/10", "Stop the other VPN"},
		},
		{
			name: "fake-ip proxy in the pool",
			s: with(lan, func(s *Snapshot) {
				s.Interfaces = append(s.Interfaces, Interface{Name: "utun9", Addrs: []netip.Prefix{pfx("198.18.0.1/16")}})
				s.Routes = append(s.Routes, Route{Prefix: pfx("198.18.0.0/16"), Dev: "utun9", Table: "main", Type: "unicast"})
			}),
			want: []string{"route 198.18.0.0/16 via utun9 overlaps 198.18.0.0/15"},
		},
		{
			name: "DNS address elsewhere",
			s: with(lan, func(s *Snapshot) {
				s.Interfaces = append(s.Interfaces, Interface{Name: "lo0", Addrs: []netip.Prefix{pfx("100.100.100.100/32")}})
			}),
			want: []string{"address 100.100.100.100/32 on lo0 overlaps 100.64.0.0/10"},
		},
		{
			name: "foreign rule at our priority",
			s: with(lan, func(s *Snapshot) {
				s.Rules = append(s.Rules, Rule{Pref: 5180, Table: "100"})
			}),
			want: []string{"ip rule priority 5180 is already used"},
		},
		{
			name: "foreign rule at an exit node priority",
			s: with(lan, func(s *Snapshot) {
				s.Rules = append(s.Rules, Rule{Pref: 5182, Table: "main"})
			}),
			want: []string{"ip rule priority 5182 is already used"},
		},
		{
			name: "foreign use of the exit table",
			s: with(lan, func(s *Snapshot) {
				s.Rules = append(s.Rules, Rule{Pref: 9000, Table: "5181"})
			}),
			want: []string{"routing table 5181 is already used"},
		},
		{
			name: "foreign use of our table",
			s: with(lan, func(s *Snapshot) {
				s.Rules = append(s.Rules, Rule{Pref: 9000, Table: "5180"})
			}),
			want: []string{"routing table 5180 is already used"},
		},
		{
			name: "own leftovers",
			s: with(lan, func(s *Snapshot) {
				s.Rules = append(s.Rules, Rule{Pref: 5180, Iif: "lo", Table: "5180"},
					Rule{Pref: 5181, Table: "main"}, Rule{Pref: 5182, Iif: "lo", Table: "main"},
					Rule{Pref: 5183, Iif: "lo", Table: "5181"})
				s.Routes = append(s.Routes,
					Route{Prefix: pfx("100.64.0.0/10"), Table: "5180", Type: "unreachable"},
					Route{Prefix: pfx("100.64.0.1/32"), Dev: "twh0", Table: "5180", Type: "unicast"},
					Route{Prefix: pfx("10.201.0.0/30"), Dev: "twh0", Table: "main", Type: "unicast"},
					Route{Prefix: pfx("100.100.100.100/32"), Dev: "tailweave0", Table: "local", Type: "local"},
					Route{Prefix: pfx("100.64.0.0/10"), Dev: "utun7"},
				)
				s.Interfaces = append(s.Interfaces,
					Interface{Name: "twh0", Addrs: []netip.Prefix{pfx("10.201.0.1/30")}},
					Interface{Name: "tailweave0", Addrs: []netip.Prefix{pfx("100.100.100.100/32")}},
					Interface{Name: "utun7", Addrs: []netip.Prefix{pfx("10.201.0.1/32")}},
				)
			}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(tc.s, claims)
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatalf("unexpected conflict: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("no conflict found")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error lacks %q: %v", w, err)
				}
			}
		})
	}
}

func TestManyPeerRoutesListedOnce(t *testing.T) {
	s := with(lan, func(s *Snapshot) {
		for i := 1; i < 50; i++ {
			s.Routes = append(s.Routes, Route{Prefix: netip.PrefixFrom(netip.AddrFrom4([4]byte{100, 64, 0, byte(i)}), 32),
				Dev: "tailscale0", Table: "52", Type: "unicast"})
		}
	})
	err := Check(s, claims)
	if err == nil || strings.Count(err.Error(), "tailscale0") != 1 {
		t.Errorf("want one mention of tailscale0: %v", err)
	}
}

func TestParseLinux(t *testing.T) {
	routes, err := parseIPRoutes([]byte(`[
		{"dst":"default","gateway":"192.168.104.2","dev":"eth0","protocol":"dhcp","flags":[]},
		{"dst":"100.64.0.1","dev":"tailscale0","table":"52","flags":[]},
		{"type":"throw","dst":"127.0.0.0/8","table":"52","flags":[]},
		{"type":"local","dst":"100.64.0.5","dev":"tailscale0","table":"local","protocol":"kernel","scope":"host","prefsrc":"100.64.0.5","flags":[]}]`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{
		{Prefix: pfx("0.0.0.0/0"), Dev: "eth0", Table: "main", Type: "unicast"},
		{Prefix: pfx("100.64.0.1/32"), Dev: "tailscale0", Table: "52", Type: "unicast"},
		{Prefix: pfx("127.0.0.0/8"), Table: "52", Type: "throw"},
		{Prefix: pfx("100.64.0.5/32"), Dev: "tailscale0", Table: "local", Type: "local"},
	}
	if !slices.Equal(routes, want) {
		t.Errorf("routes = %+v, want %+v", routes, want)
	}

	rules, err := parseIPRules([]byte(`[{"priority":0,"src":"all","table":"local"},
		{"priority":5180,"src":"all","iif":"lo","table":"5180"},
		{"priority":5210,"src":"all","fwmark":"0x80000/0xff0000","table":"main"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []Rule{{0, "", "local"}, {5180, "lo", "5180"}, {5210, "", "main"}}; !slices.Equal(rules, want) {
		t.Errorf("rules = %+v, want %+v", rules, want)
	}

	ifs, err := parseIPAddrs([]byte(`[{"ifindex":1,"ifname":"lo","addr_info":[{"family":"inet","local":"127.0.0.1","prefixlen":8}]},
		{"ifindex":5,"ifname":"tailscale0","addr_info":[{"family":"inet","local":"100.64.0.5","prefixlen":32}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ifs) != 2 || ifs[1].Name != "tailscale0" || ifs[1].Addrs[0] != pfx("100.64.0.5/32") {
		t.Errorf("interfaces = %+v", ifs)
	}
}

func TestParseDarwin(t *testing.T) {
	routes := parseNetstat(`Routing tables

Internet:
Destination        Gateway            Flags               Netif Expire
default            192.168.1.1        UGScg                 en0
default            link#22            UCSIg               utun4
100.64/10          link#22            UCSI                utun4
100.100.100.100/32 link#22            UCS                 utun4
127                127.0.0.1          UCS                   lo0
169.254            link#14            UCS                   en0      !
192.168.1          link#14            UCS                   en0      !
192.168.1.1        34:e1:a9:9a:f1:1   UHLWIir               en0   1192
224.0.0/4          link#14            UmCS                  en0      !
`)
	want := []Route{
		{Prefix: pfx("0.0.0.0/0"), Dev: "en0"},
		{Prefix: pfx("0.0.0.0/0"), Dev: "utun4"},
		{Prefix: pfx("100.64.0.0/10"), Dev: "utun4"},
		{Prefix: pfx("100.100.100.100/32"), Dev: "utun4"},
		{Prefix: pfx("127.0.0.0/8"), Dev: "lo0"},
		{Prefix: pfx("169.254.0.0/16"), Dev: "en0"},
		{Prefix: pfx("192.168.1.0/24"), Dev: "en0"},
		{Prefix: pfx("192.168.1.1/32"), Dev: "en0"},
		{Prefix: pfx("224.0.0.0/4"), Dev: "en0"},
	}
	if !slices.Equal(routes, want) {
		t.Errorf("routes = %+v, want %+v", routes, want)
	}

	ifs := parseIfconfig(`lo0: flags=8049<UP,LOOPBACK,RUNNING,MULTICAST> mtu 16384
	options=1203<RXCSUM,TXCSUM,TXSTATUS,SW_TIMESTAMP>
	inet 127.0.0.1 netmask 0xff000000
	inet6 ::1 prefixlen 128
en0: flags=8863<UP,BROADCAST,SMART,RUNNING,SIMPLEX,MULTICAST> mtu 1500
	ether 72:7e:c9:71:e0:8a
	inet 192.168.1.20 netmask 0xffffff00 broadcast 192.168.1.255
utun4: flags=8051<UP,POINTOPOINT,RUNNING,MULTICAST> mtu 1280
	inet 100.101.102.103 --> 100.101.102.103 netmask 0xffffffff
`)
	if len(ifs) != 3 || ifs[0].Addrs[0] != pfx("127.0.0.1/8") || ifs[1].Addrs[0] != pfx("192.168.1.20/24") ||
		ifs[2].Name != "utun4" || ifs[2].Addrs[0] != pfx("100.101.102.103/32") {
		t.Errorf("interfaces = %+v", ifs)
	}
}

func TestHostNetworks(t *testing.T) {
	s := with(lan, func(s *Snapshot) {
		s.Routes = append(s.Routes,
			Route{Prefix: pfx("10.8.0.0/16"), Dev: "wg0", Table: "main", Type: "unicast"},
			Route{Prefix: pfx("10.99.0.0/24"), Dev: "tailweave0", Table: "5180", Type: "unicast"},
			Route{Prefix: pfx("198.18.0.0/15"), Dev: "twh0", Table: "main", Type: "unicast"},
			Route{Prefix: pfx("0.0.0.0/1"), Dev: "tun0", Table: "main", Type: "unicast"},
			Route{Prefix: pfx("10.50.0.0/16"), Table: "main", Type: "throw"},
			Route{Prefix: pfx("172.16.0.0/12"), Dev: "eth1", Table: "100", Type: "unicast"},
		)
		s.Interfaces = append(s.Interfaces,
			Interface{Name: "docker0", Addrs: []netip.Prefix{pfx("172.17.0.1/16")}},
			Interface{Name: "utun5", Addrs: []netip.Prefix{pfx("10.201.0.1/32")}},
		)
	})
	want := []netip.Prefix{pfx("127.0.0.0/8"), pfx("192.168.1.0/24"), pfx("172.17.0.0/16"), pfx("10.8.0.0/16")}
	if got := HostNetworks(s, claims); !slices.Equal(got, want) {
		t.Errorf("HostNetworks = %v, want %v", got, want)
	}
}

func TestCheckExit(t *testing.T) {
	halves := func(dev string) func(*Snapshot) {
		return func(s *Snapshot) {
			s.Routes = append(s.Routes,
				Route{Prefix: pfx("0.0.0.0/1"), Dev: dev, Table: "main", Type: "unicast"},
				Route{Prefix: pfx("128.0.0.0/1"), Dev: dev, Table: "main", Type: "unicast"})
			s.Interfaces = append(s.Interfaces, Interface{Name: dev, Addrs: []netip.Prefix{pfx("10.201.0.1/32")}})
		}
	}
	if err := CheckExit(lan, claims); err != nil {
		t.Errorf("plain default route: %v", err)
	}
	// tailweave's own utun, recognized by the transit address.
	if err := CheckExit(with(lan, halves("utun7")), claims); err != nil {
		t.Errorf("own halves: %v", err)
	}
	wg := with(lan, func(s *Snapshot) {
		s.Routes = append(s.Routes, Route{Prefix: pfx("0.0.0.0/1"), Dev: "wg0", Table: "main", Type: "unicast"})
	})
	if err := CheckExit(wg, claims); err == nil || !strings.Contains(err.Error(), "through wg0") {
		t.Errorf("WireGuard full tunnel: %v", err)
	}
}

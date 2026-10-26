package router

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"github.com/Nikiosna/tailweave/internal/weave"
)

const (
	protoICMP = 1 // ICMPv6 (58) for IPv6 packets
	protoTCP  = 6
	protoUDP  = 17
)

var (
	hostIP = netip.MustParseAddr("10.201.0.1")
	selfA  = netip.MustParseAddr("100.64.0.3")
	selfB  = netip.MustParseAddr("100.64.0.3") // same as A: both tailnets use the same range
	peer   = netip.MustParseAddr("100.64.0.1") // exists in both tailnets
	mapped = netip.MustParseAddr("198.18.0.1") // B's 100.64.0.1 on this machine
	onlyA  = netip.MustParseAddr("100.64.0.2")

	hostIP6 = netip.MustParseAddr("fd74:7765:6176::1")
	selfA6  = netip.MustParseAddr("fd7a:115c:a1e0::3")
	selfB6  = netip.MustParseAddr("fd7a:115c:a1e0::3")

	labNet  = netip.MustParsePrefix("10.99.0.0/24")                     // routed by both tailnets
	block   = netip.MustParsePrefix("198.18.1.0/24")                    // b's 10.99.0.0/24 on this machine
	via7    = netip.MustParsePrefix("fd7a:115c:a1e0:b1a:0:7:a63:0/120") // a's 4via6 route
	inSub   = netip.MustParseAddr("10.99.0.7")
	inBlock = netip.MustParseAddr("198.18.1.7")
	inVia   = netip.MustParseAddr("fd7a:115c:a1e0:b1a:0:7:a63:1")
)

// lab is a router with tailnets a and b, where a's 100.64.0.1 keeps its IP and b's is
// mapped to 198.18.0.1. Both route 10.99.0.0/24, b's at 198.18.1.0/24, and a has a
// 4via6 route.
type lab struct {
	r      *Router
	host   *device     // what the router reads from and writes to as the host TUN
	toHost chan []byte // packets the router wrote to the host
	a, b   *device     // the tailnets' devices, from the engines' point of view
	fromA  chan []byte // packets the router queued for a
}

func newLab(t *testing.T) *lab {
	t.Helper()
	l := &lab{toHost: make(chan []byte, 16)}
	l.host = newDevice(func(pkt []byte) { l.toHost <- append([]byte(nil), pkt...) })
	l.r = New(l.host, hostIP, hostIP6, []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("198.18.0.0/15"), weave.ViaRange})
	l.a = l.r.AddTailnet("a").(*device)
	l.b = l.r.AddTailnet("b").(*device)
	l.r.SetSelf("a", selfA, selfA6)
	l.r.SetSelf("b", selfB, selfB6)
	l.r.SetPlan(l.plan())
	done := make(chan error, 1)
	go func() { done <- l.r.Run() }()
	t.Cleanup(func() {
		l.host.Close()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	return l
}

// plan is the lab's plan.
func (l *lab) plan() weave.Plan {
	return weave.Plan{Routes: map[netip.Addr]weave.Target{
		peer:   {Tailnet: "a", IP: peer},
		onlyA:  {Tailnet: "a", IP: onlyA},
		mapped: {Tailnet: "b", IP: peer},
	}, Subnets: map[netip.Prefix]weave.SubnetTarget{
		labNet: {Tailnet: "a", Prefix: labNet},
		block:  {Tailnet: "b", Prefix: labNet},
		via7:   {Tailnet: "a", Prefix: via7},
	}}
}

// send makes the host send pkt into the router.
func (l *lab) send(pkt []byte) { l.host.queue(pkt) }

// recv waits for a packet on d's queue (sent towards a tailnet's engine).
func recv(t *testing.T, d *device) []byte {
	t.Helper()
	select {
	case p := <-d.in:
		return p
	case <-time.After(2 * time.Second):
		t.Fatal("no packet")
		return nil
	}
}

// recvHost waits for a packet the router wrote to the host.
func recvHost(t *testing.T, l *lab) []byte {
	t.Helper()
	select {
	case p := <-l.toHost:
		return p
	case <-time.After(2 * time.Second):
		t.Fatal("no packet for the host")
		return nil
	}
}

func none(t *testing.T, ch <-chan []byte) {
	t.Helper()
	select {
	case p := <-ch:
		t.Fatalf("unexpected packet % x", p)
	case <-time.After(100 * time.Millisecond):
	}
}

// reply makes a tailnet's engine send pkt to the router.
func reply(d *device, pkt []byte) {
	buf := make([]byte, 16+len(pkt))
	copy(buf[16:], pkt)
	d.Write([][]byte{buf}, 16)
}

func TestRoutesByPlan(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proto byte
	}{{"tcp", protoTCP}, {"udp", protoUDP}, {"icmp", protoICMP}} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t)

			// 100.64.0.1 goes to a unchanged, apart from the source.
			l.send(build(tc.proto, hostIP, peer, 40000, 80, false))
			got := recv(t, l.a)
			check(t, got, tc.proto, selfA, peer)
			none(t, l.b.in)

			// 198.18.0.1 goes to b as 100.64.0.1.
			l.send(build(tc.proto, hostIP, mapped, 40001, 80, false))
			got = recv(t, l.b)
			check(t, got, tc.proto, selfB, peer)
			none(t, l.a.in)

			// b's answer reaches the host from 198.18.0.1.
			reply(l.b, build(tc.proto, peer, selfB, 80, 40001, true))
			check(t, <-l.toHost, tc.proto, mapped, hostIP)

			// a's answer from 100.64.0.1 stays 100.64.0.1.
			reply(l.a, build(tc.proto, peer, selfA, 80, 40000, true))
			check(t, <-l.toHost, tc.proto, peer, hostIP)
		})
	}
}

func TestDropsUnroutedAndForeign(t *testing.T) {
	l := newLab(t)
	// Not in the plan.
	l.send(build(protoUDP, hostIP, netip.MustParseAddr("100.64.0.9"), 1, 2, false))
	// Not from the host's address: forwarded traffic must not get in.
	l.send(build(protoUDP, netip.MustParseAddr("192.168.5.20"), peer, 1, 2, false))
	none(t, l.a.in)
	none(t, l.b.in)
}

func TestInbound(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proto byte
	}{{"tcp", protoTCP}, {"udp", protoUDP}, {"icmp", protoICMP}} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t)

			// a's 100.64.0.1 connects to the host and arrives from its own IP.
			reply(l.a, build(tc.proto, peer, selfA, 50000, 22, false))
			check(t, recvHost(t, l), tc.proto, peer, hostIP)

			// The host's answer goes back to a.
			l.send(build(tc.proto, hostIP, peer, 22, 50000, true))
			check(t, recv(t, l.a), tc.proto, selfA, peer)
			none(t, l.b.in)

			// b's 100.64.0.1 arrives from the IP it is routed at, and the answer reaches b.
			reply(l.b, build(tc.proto, peer, selfB, 50000, 22, false))
			check(t, recvHost(t, l), tc.proto, mapped, hostIP)
			l.send(build(tc.proto, hostIP, mapped, 22, 50000, true))
			check(t, recv(t, l.b), tc.proto, selfB, peer)
			none(t, l.a.in)
		})
	}
}

func TestInboundDrops(t *testing.T) {
	l := newLab(t)
	// A peer that is not routed on this machine.
	reply(l.a, build(protoTCP, netip.MustParseAddr("100.64.0.9"), selfA, 50000, 22, false))
	// Packets for anything but the engine's own IP: other peers, the host's or another
	// tailnet's addresses, the LAN.
	for _, dst := range []string{"100.64.0.2", "198.18.0.1", "10.201.0.1", "192.168.5.20"} {
		reply(l.a, build(protoTCP, peer, netip.MustParseAddr(dst), 50000, 22, false))
		reply(l.a, build(protoUDP, peer, netip.MustParseAddr(dst), 50000, 53, false))
		reply(l.a, build(protoICMP, peer, netip.MustParseAddr(dst), 0, 0, false))
	}
	// Other ICMP messages and other protocols.
	unreach := build(protoICMP, peer, selfA, 0, 0, false)
	unreach[20] = 3
	fixChecksums(unreach)
	reply(l.a, unreach)
	gre := build(protoUDP, peer, selfA, 50000, 53, false)
	gre[9] = 47
	fixChecksums(gre)
	reply(l.a, gre)
	none(t, l.toHost)
	none(t, l.a.in)
	none(t, l.b.in)
}

func TestRemoveTailnet(t *testing.T) {
	l := newLab(t)
	l.send(build(protoUDP, hostIP, peer, 40000, 8, false))
	recv(t, l.a)
	l.r.RemoveTailnet("a")
	l.send(build(protoUDP, hostIP, peer, 40000, 8, false))
	none(t, l.a.in)
	if _, err := l.a.Write([][]byte{make([]byte, 40)}, 0); err == nil {
		t.Error("write to removed tailnet's device succeeded")
	}
}

func TestNoSelfIPYet(t *testing.T) {
	l := newLab(t)
	a := l.r.AddTailnet("a").(*device) // fresh port without a self IP
	l.send(build(protoUDP, hostIP, peer, 40000, 8, false))
	none(t, a.in)
}

func TestSubnetRoutes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proto byte
	}{{"tcp", protoTCP}, {"udp", protoUDP}, {"icmp", protoICMP}} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t)

			// a's route keeps its prefix.
			l.send(build(tc.proto, hostIP, inSub, 40000, 80, false))
			check(t, recv(t, l.a), tc.proto, selfA, inSub)
			none(t, l.b.in)
			reply(l.a, build(tc.proto, inSub, selfA, 80, 40000, true))
			check(t, recvHost(t, l), tc.proto, inSub, hostIP)

			// b's is reachable in its block, with the same host part.
			l.send(build(tc.proto, hostIP, inBlock, 40001, 80, false))
			check(t, recv(t, l.b), tc.proto, selfB, inSub)
			none(t, l.a.in)
			reply(l.b, build(tc.proto, inSub, selfB, 80, 40001, true))
			check(t, recvHost(t, l), tc.proto, inBlock, hostIP)
		})
	}
}

func TestViaRoutes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proto byte
	}{{"tcp", protoTCP}, {"udp", protoUDP}, {"icmp", protoICMP}} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t)
			l.send(build(tc.proto, hostIP6, inVia, 40000, 80, false))
			check(t, recv(t, l.a), tc.proto, selfA6, inVia)
			none(t, l.b.in)
			reply(l.a, build(tc.proto, inVia, selfA6, 80, 40000, true))
			check(t, recvHost(t, l), tc.proto, inVia, hostIP6)
		})
	}
}

func TestDropsIPv6(t *testing.T) {
	l := newLab(t)
	// Not from the host's IPv6 address, or not routed.
	l.send(build(protoUDP, netip.MustParseAddr("fd00::5"), inVia, 1, 2, false))
	l.send(build(protoUDP, hostIP6, netip.MustParseAddr("fd7a:115c:a1e0:b1a:0:8:a63:1"), 1, 2, false))
	l.send(build(protoUDP, hostIP6, netip.MustParseAddr("fd7a:115c:a1e0::1"), 1, 2, false))
	none(t, l.a.in)
	none(t, l.b.in)
	// From a tailnet: only to the engine's own IPv6, only from routed addresses.
	reply(l.a, build(protoUDP, inVia, netip.MustParseAddr("fd7a:115c:a1e0::9"), 1, 2, false))
	reply(l.a, build(protoUDP, netip.MustParseAddr("fd7a:115c:a1e0::1"), selfA6, 1, 2, false))
	none(t, l.toHost)

	// Without an IPv6 address on the host, nothing IPv6 gets through.
	l.r.hostIP6 = netip.Addr{}
	reply(l.a, build(protoUDP, inVia, selfA6, 1, 2, false))
	none(t, l.toHost)
}

func TestNoSelfIPv6Yet(t *testing.T) {
	l := newLab(t)
	l.r.SetSelf("a", selfA, netip.Addr{})
	l.send(build(protoUDP, hostIP6, inVia, 40000, 8, false))
	none(t, l.a.in)
	// IPv4 still works.
	l.send(build(protoUDP, hostIP, inSub, 40000, 8, false))
	check(t, recv(t, l.a), protoUDP, selfA, inSub)
}

func TestLongestPrefix(t *testing.T) {
	l := newLab(t)
	wide := netip.MustParsePrefix("10.0.0.0/8")
	l.r.SetPlan(weave.Plan{Subnets: map[netip.Prefix]weave.SubnetTarget{
		wide:   {Tailnet: "b", Prefix: wide},
		labNet: {Tailnet: "a", Prefix: labNet},
	}})
	l.send(build(protoUDP, hostIP, inSub, 1, 2, false))
	check(t, recv(t, l.a), protoUDP, selfA, inSub)
	l.send(build(protoUDP, hostIP, netip.MustParseAddr("10.98.0.7"), 1, 2, false))
	check(t, recv(t, l.b), protoUDP, selfB, netip.MustParseAddr("10.98.0.7"))
}

func TestTranslate(t *testing.T) {
	for _, tc := range []struct{ a, to, want string }{
		{"10.99.0.7", "198.18.1.0/24", "198.18.1.7"},
		{"10.99.0.135", "198.18.2.128/25", "198.18.2.135"},
		{"10.99.3.7", "198.18.4.0/22", "198.18.7.7"},
		{"10.99.0.7", "198.18.1.9/32", "198.18.1.9"},
		{"fd7a:115c:a1e0:b1a:0:7:a63:1", "fd7a:115c:a1e0:b1a:0:9:a63:0/120", "fd7a:115c:a1e0:b1a:0:9:a63:1"},
	} {
		if got := translate(netip.MustParseAddr(tc.a), netip.MustParsePrefix(tc.to)); got.String() != tc.want {
			t.Errorf("translate(%s, %s) = %s, want %s", tc.a, tc.to, got, tc.want)
		}
	}
	if got := translate(netip.MustParseAddr("10.99.0.7"), via7); got.IsValid() {
		t.Errorf("translate across families = %s", got)
	}
}

// build returns an IPv4 or IPv6 packet with valid checksums, depending on src. For ICMP,
// ports are ignored and the packet is an echo request (or reply if answer is set) with
// ID 0x1234.
func build(proto byte, src, dst netip.Addr, sport, dport uint16, answer bool) []byte {
	if src.Is6() {
		return build6(proto, src, dst, sport, dport, answer)
	}
	l4 := buildL4(proto, sport, dport, answer, false)
	p := make([]byte, 20+len(l4))
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
	p[8] = 64
	p[9] = proto
	s, d := src.As4(), dst.As4()
	copy(p[12:], s[:])
	copy(p[16:], d[:])
	copy(p[20:], l4)
	fixChecksums(p)
	return p
}

// build6 is build for IPv6; ICMP is ICMPv6.
func build6(proto byte, src, dst netip.Addr, sport, dport uint16, answer bool) []byte {
	l4 := buildL4(proto, sport, dport, answer, true)
	p := make([]byte, 40+len(l4))
	p[0] = 0x60
	binary.BigEndian.PutUint16(p[4:], uint16(len(l4)))
	p[6] = next(proto, true)
	p[7] = 64
	s, d := src.As16(), dst.As16()
	copy(p[8:], s[:])
	copy(p[24:], d[:])
	copy(p[40:], l4)
	fixChecksums(p)
	return p
}

// next returns the IP protocol number of proto, which is ICMPv6 for ICMP over IPv6.
func next(proto byte, v6 bool) byte {
	if proto == protoICMP && v6 {
		return 58
	}
	return proto
}

func buildL4(proto byte, sport, dport uint16, answer, v6 bool) []byte {
	var l4 []byte
	switch proto {
	case protoTCP:
		l4 = make([]byte, 20+4)
		binary.BigEndian.PutUint16(l4[0:], sport)
		binary.BigEndian.PutUint16(l4[2:], dport)
		l4[12] = 5 << 4
		l4[13] = 0x02 // SYN
		if answer {
			l4[13] = 0x12 // SYN+ACK
		}
		copy(l4[20:], "ping")
	case protoUDP:
		l4 = make([]byte, 8+4)
		binary.BigEndian.PutUint16(l4[0:], sport)
		binary.BigEndian.PutUint16(l4[2:], dport)
		binary.BigEndian.PutUint16(l4[4:], uint16(len(l4)))
		copy(l4[8:], "ping")
	case protoICMP:
		l4 = make([]byte, 8+4)
		l4[0] = 8
		if answer {
			l4[0] = 0
		}
		if v6 {
			l4[0] = 128
			if answer {
				l4[0] = 129
			}
		}
		binary.BigEndian.PutUint16(l4[4:], 0x1234)
		binary.BigEndian.PutUint16(l4[6:], 1)
		copy(l4[8:], "ping")
	}
	return l4
}

// fixChecksums computes all checksums of an IPv4 or IPv6 packet from scratch.
func fixChecksums(p []byte) {
	if p[0]>>4 == 6 {
		l4 := p[40:]
		off := map[byte]int{58: 2, protoTCP: 16, protoUDP: 6}[p[6]]
		binary.BigEndian.PutUint16(l4[off:], 0)
		binary.BigEndian.PutUint16(l4[off:], ^sum(pseudo6(p), l4))
		return
	}
	binary.BigEndian.PutUint16(p[10:], 0)
	binary.BigEndian.PutUint16(p[10:], ^sum(0, p[:20]))
	l4 := p[20:]
	switch p[9] {
	case protoICMP:
		binary.BigEndian.PutUint16(l4[2:], 0)
		binary.BigEndian.PutUint16(l4[2:], ^sum(0, l4))
	case protoTCP, protoUDP:
		off := 16
		if p[9] == protoUDP {
			off = 6
		}
		binary.BigEndian.PutUint16(l4[off:], 0)
		binary.BigEndian.PutUint16(l4[off:], ^sum(pseudo(p), l4))
	}
}

func pseudo(p []byte) uint32 {
	var s uint32
	for i := 12; i < 20; i += 2 {
		s += uint32(binary.BigEndian.Uint16(p[i:]))
	}
	return s + uint32(p[9]) + uint32(len(p)-20)
}

func pseudo6(p []byte) uint32 {
	var s uint32
	for i := 8; i < 40; i += 2 {
		s += uint32(binary.BigEndian.Uint16(p[i:]))
	}
	return s + uint32(p[6]) + uint32(len(p)-40)
}

func sum(s uint32, b []byte) uint16 {
	for len(b) >= 2 {
		s += uint32(binary.BigEndian.Uint16(b))
		b = b[2:]
	}
	if len(b) == 1 {
		s += uint32(b[0]) << 8
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return uint16(s)
}

// check verifies addresses and that every checksum is still valid.
func check(t *testing.T, p []byte, proto byte, src, dst netip.Addr) {
	t.Helper()
	if p[0]>>4 == 6 {
		if p[6] != next(proto, true) {
			t.Fatalf("next header %d, want %d", p[6], next(proto, true))
		}
		if got := netip.AddrFrom16([16]byte(p[8:24])); got != src {
			t.Errorf("source %s, want %s", got, src)
		}
		if got := netip.AddrFrom16([16]byte(p[24:40])); got != dst {
			t.Errorf("destination %s, want %s", got, dst)
		}
		want := append([]byte(nil), p...)
		fixChecksums(want)
		if string(want) != string(p) {
			t.Errorf("checksums wrong after rewrite:\n got % x\nwant % x", p, want)
		}
		return
	}
	if p[9] != proto {
		t.Fatalf("protocol %d, want %d", p[9], proto)
	}
	if got := netip.AddrFrom4([4]byte(p[12:16])); got != src {
		t.Errorf("source %s, want %s", got, src)
	}
	if got := netip.AddrFrom4([4]byte(p[16:20])); got != dst {
		t.Errorf("destination %s, want %s", got, dst)
	}
	want := append([]byte(nil), p...)
	fixChecksums(want)
	if string(want) != string(p) {
		t.Errorf("checksums wrong after rewrite:\n got % x\nwant % x", p, want)
	}
}

func TestExitNode(t *testing.T) {
	l := newLab(t)
	internet := netip.MustParseAddr("198.51.100.10")

	// Without an exit node, internet traffic goes nowhere.
	l.send(build(protoTCP, hostIP, internet, 40000, 80, false))
	none(t, l.a.in)
	none(t, l.b.in)

	p := l.plan()
	p.Exit = &weave.Exit{Tailnet: "b", PeerID: "4", Name: "exit-b"}
	l.r.SetPlan(p)
	for _, proto := range []byte{protoTCP, protoUDP, protoICMP} {
		// Into b, to the destination as it is, from b's own IP.
		l.send(build(proto, hostIP, internet, 40000, 80, false))
		check(t, recv(t, l.b), proto, selfB, internet)
		none(t, l.a.in)
		// The answer reaches the host from the internet address.
		reply(l.b, build(proto, internet, selfB, 80, 40000, true))
		check(t, recvHost(t, l), proto, internet, hostIP)
	}
	// Tailnet traffic still goes by the plan.
	l.send(build(protoTCP, hostIP, peer, 40000, 80, false))
	check(t, recv(t, l.a), protoTCP, selfA, peer)

	// Never through the exit node: unrouted tailnet and pool addresses, 4via6, multicast,
	// IPv6 (not supported yet).
	for _, dst := range []string{"100.64.0.9", "198.18.5.5", "224.0.0.251", "255.255.255.255"} {
		l.send(build(protoUDP, hostIP, netip.MustParseAddr(dst), 1, 2, false))
	}
	l.send(build(protoUDP, hostIP6, netip.MustParseAddr("2001:db8::1"), 1, 2, false))
	l.send(build(protoUDP, hostIP6, netip.MustParseAddr("fd7a:115c:a1e0:b1a:0:9:a63:1"), 1, 2, false))
	none(t, l.a.in)
	none(t, l.b.in)

	// Only the exit node's tailnet answers from the internet, and nobody from tailweave's
	// own ranges: an unrouted peer, a pool address.
	reply(l.a, build(protoTCP, internet, selfA, 80, 40000, true))
	reply(l.b, build(protoTCP, netip.MustParseAddr("100.64.0.9"), selfB, 80, 40000, true))
	reply(l.b, build(protoTCP, netip.MustParseAddr("198.18.0.77"), selfB, 80, 40000, true))
	none(t, l.toHost)

	// The exit node's tailnet is not attached: blocked, not direct.
	p.Exit = &weave.Exit{Tailnet: "c", PeerID: "4", Name: "exit-c"}
	l.r.SetPlan(p)
	l.send(build(protoTCP, hostIP, internet, 40000, 80, false))
	none(t, l.a.in)
	none(t, l.b.in)
}

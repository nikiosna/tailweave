package router

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"github.com/Nikiosna/tailweave/internal/weave"
)

const (
	protoICMP = 1
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
)

// lab is a router with tailnets a and b, where a's 100.64.0.1 keeps its IP and b's is
// mapped to 198.18.0.1.
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
	l.r = New(l.host, hostIP)
	l.a = l.r.AddTailnet("a").(*device)
	l.b = l.r.AddTailnet("b").(*device)
	l.r.SetSelf("a", selfA)
	l.r.SetSelf("b", selfB)
	l.r.SetPlan(weave.Plan{Routes: map[netip.Addr]weave.Target{
		peer:   {Tailnet: "a", IP: peer},
		onlyA:  {Tailnet: "a", IP: onlyA},
		mapped: {Tailnet: "b", IP: peer},
	}})
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

func TestDropsNewFlowsFromTailnet(t *testing.T) {
	l := newLab(t)
	// A peer opening a connection to the host.
	reply(l.a, build(protoTCP, peer, selfA, 50000, 22, false))
	reply(l.a, build(protoUDP, peer, selfA, 50000, 53, false))
	reply(l.a, build(protoICMP, peer, selfA, 7, 7, false))
	none(t, l.toHost)

	// An answer to a's flow coming from b's peer with the same IP.
	l.send(build(protoUDP, hostIP, peer, 40000, 8, false))
	recv(t, l.a)
	reply(l.b, build(protoUDP, peer, selfB, 8, 40000, true))
	none(t, l.toHost)

	// A peer that is not routed on this machine.
	reply(l.a, build(protoUDP, netip.MustParseAddr("100.64.0.9"), selfA, 8, 40000, true))
	none(t, l.toHost)
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

// build returns an IPv4 packet with valid checksums. For ICMP, ports are ignored and the
// packet is an echo request (or reply if answer is set) with ID 0x1234.
func build(proto byte, src, dst netip.Addr, sport, dport uint16, answer bool) []byte {
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
		binary.BigEndian.PutUint16(l4[4:], 0x1234)
		binary.BigEndian.PutUint16(l4[6:], 1)
		copy(l4[8:], "ping")
	}
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

// fixChecksums computes all checksums of an IPv4 packet from scratch.
func fixChecksums(p []byte) {
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

package router

import (
	"encoding/binary"
	"net/netip"
	"sync"
	"time"

	"tailscale.com/net/packet"
	"tailscale.com/types/ipproto"
)

// Idle times after which a flow is forgotten and answers to it are dropped.
const (
	tcpIdle   = 30 * time.Minute
	otherIdle = 2 * time.Minute
)

// flowKey identifies a connection the host opened into a tailnet, by the tailnet side's
// view of it: the peer's real address and the host's port. ICMP echo uses the echo ID as
// the port on both sides.
type flowKey struct {
	tailnet string
	proto   ipproto.Proto
	peer    netip.AddrPort
	local   uint16
}

// flows remembers which connections the host opened, so packets from a tailnet are only
// let through when they answer one of them.
type flows struct {
	mu   sync.Mutex
	seen map[flowKey]time.Time
	now  func() time.Time
}

func newFlows() *flows {
	return &flows{seen: map[flowKey]time.Time{}, now: time.Now}
}

// outKey is the key of a packet from the host to peer (after DNAT), ok is false for
// packets that start no trackable flow.
func outKey(tailnet string, q *packet.Parsed) (flowKey, bool) {
	switch q.IPProto {
	case ipproto.TCP, ipproto.UDP:
		return flowKey{tailnet, q.IPProto, q.Dst, q.Src.Port()}, true
	case ipproto.ICMPv4:
		if q.IsEchoRequest() {
			id := echoID(q)
			return flowKey{tailnet, q.IPProto, netip.AddrPortFrom(q.Dst.Addr(), id), id}, true
		}
	}
	return flowKey{}, false
}

// inKey is the key of a packet from peer to the host (before reverse NAT).
func inKey(tailnet string, q *packet.Parsed) (flowKey, bool) {
	switch q.IPProto {
	case ipproto.TCP, ipproto.UDP:
		return flowKey{tailnet, q.IPProto, q.Src, q.Dst.Port()}, true
	case ipproto.ICMPv4:
		if q.IsEchoResponse() {
			id := echoID(q)
			return flowKey{tailnet, q.IPProto, netip.AddrPortFrom(q.Src.Addr(), id), id}, true
		}
	}
	return flowKey{}, false
}

func (f *flows) add(k flowKey) {
	f.mu.Lock()
	f.seen[k] = f.now()
	f.mu.Unlock()
}

// allowed reports whether k answers a known flow and refreshes it.
func (f *flows) allowed(k flowKey) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.seen[k]
	if !ok {
		return false
	}
	now := f.now()
	if now.Sub(t) > idle(k.proto) {
		delete(f.seen, k)
		return false
	}
	f.seen[k] = now
	return true
}

// echoID returns the identifier of an ICMP echo request or reply; the Is* checks made
// sure the header is there.
func echoID(q *packet.Parsed) uint16 {
	return binary.BigEndian.Uint16(q.Transport()[4:6])
}

// expire forgets idle flows and all flows of tailnet.
func (f *flows) expire(tailnet string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	for k, t := range f.seen {
		if k.tailnet == tailnet || now.Sub(t) > idle(k.proto) {
			delete(f.seen, k)
		}
	}
}

func idle(p ipproto.Proto) time.Duration {
	if p == ipproto.TCP {
		return tcpIdle
	}
	return otherIdle
}

// Package router is tailweave's userspace switch between the host and its tailnets.
//
// The host sends traffic for the tailnets into a single TUN device. The router looks up
// every packet's destination in the weave plan and hands it to the device of the
// tailnet's Tailscale engine, rewriting the destination from the IP the device is routed
// at on this machine to its real tailnet IP, and the source from the host's TUN address
// to the engine's own tailnet IP. Answers get the reverse treatment.
//
// Subnet routes work the same way per prefix: a remapped block keeps the host part of
// an address and swaps the prefix. 4via6 routes are IPv6; those packets come from the
// host's IPv6 TUN address and leave with the engine's own tailnet IPv6.
//
// Peers reach the host too, like with Tailscale: a packet a tailnet sends to its engine's
// own IP goes to the host's TUN address, from the IP the peer is routed at on this
// machine. Which peers may connect to which ports is up to the tailnet's ACLs, which the
// engine enforces. Nothing else from a tailnet gets through: neither packets for other
// addresses nor packets from peers that are not routed, so no tailnet reaches another one
// or anything beyond the host. As the router keeps no state, hosts behind a routed subnet
// route can reach the host as well, as with Tailscale's --accept-routes, if their subnet
// router does not masquerade them.
//
// The address translation is 1:1 and needs no state.
package router

import (
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/Nikiosna/tailweave/internal/weave"
	"github.com/tailscale/wireguard-go/tun"
	"tailscale.com/net/packet"
	"tailscale.com/net/packet/checksum"
	"tailscale.com/types/ipproto"
)

// headroom is the space kept in front of packets written to the host TUN; Linux TUNs with
// offloads need room for a virtio header there.
const headroom = 16

// Router moves packets between the host TUN and the tailnets' devices.
type Router struct {
	host    tun.Device
	hostIP  netip.Addr
	hostIP6 netip.Addr // zero: no IPv6

	writeMu sync.Mutex // serializes writes to host

	mu    sync.Mutex // guards ports and plan, and rebuilding tables
	ports map[string]*port
	plan  weave.Plan
	tab   atomic.Pointer[tables]
}

// port is a tailnet's attachment to the router.
type port struct {
	name        string
	dev         *device
	self, self6 netip.Addr // the engine's own tailnet IPv4 and IPv6; zero until known
}

// selfFor returns the engine's own address in the family of a.
func (p *port) selfFor(a netip.Addr) netip.Addr {
	if a.Is4() {
		return p.self
	}
	return p.self6
}

// tables is an immutable snapshot of everything needed to route a packet.
type tables struct {
	out     map[netip.Addr]target                // routed IP -> tailnet and real IP
	in      map[string]map[netip.Addr]netip.Addr // tailnet -> real IP -> routed IP
	subnets []subnet                             // routed prefixes, longest first
	ports   map[string]*port
}

type target struct {
	port *port
	real netip.Addr
}

// subnet is a routed prefix and the real route it stands for in a tailnet.
type subnet struct {
	routed, real netip.Prefix
	port         *port
}

// New returns a router for the host TUN whose addresses on the host are hostIP and, for
// IPv6, hostIP6 (may be zero). Call Run to start moving packets.
func New(host tun.Device, hostIP, hostIP6 netip.Addr) *Router {
	r := &Router{host: host, hostIP: hostIP, hostIP6: hostIP6, ports: map[string]*port{}}
	r.rebuild()
	return r
}

// AddTailnet attaches a tailnet and returns the device its engine has to use. A tailnet
// attached before is detached first.
func (r *Router) AddTailnet(name string) tun.Device {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.ports[name]; p != nil {
		p.dev.Close()
	}
	p := &port{name: name}
	p.dev = newDevice(func(pkt []byte) { r.fromTailnet(p, pkt) })
	r.ports[name] = p
	r.rebuildLocked()
	return p.dev
}

// RemoveTailnet detaches a tailnet.
func (r *Router) RemoveTailnet(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.ports[name]; p != nil {
		p.dev.Close()
		delete(r.ports, name)
		r.rebuildLocked()
	}
}

// SetSelf records a tailnet's own IPv4 and IPv6 address, the sources of everything the
// host sends into it and the only destinations the host receives packets for from it.
func (r *Router) SetSelf(name string, ip, ip6 netip.Addr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.ports[name]; p != nil && (p.self != ip || p.self6 != ip6) {
		// Ports are shared with the current snapshot, so replace instead of mutating.
		np := &port{name: name, dev: p.dev, self: ip, self6: ip6}
		r.ports[name] = np
		r.rebuildLocked()
	}
}

// SetPlan makes the router route by p.
func (r *Router) SetPlan(p weave.Plan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plan = p
	r.rebuildLocked()
}

func (r *Router) rebuild() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rebuildLocked()
}

func (r *Router) rebuildLocked() {
	t := &tables{
		out:   map[netip.Addr]target{},
		in:    map[string]map[netip.Addr]netip.Addr{},
		ports: map[string]*port{},
	}
	for name, p := range r.ports {
		t.ports[name] = p
		t.in[name] = map[netip.Addr]netip.Addr{}
	}
	for ip, tg := range r.plan.Routes {
		p := r.ports[tg.Tailnet]
		if p == nil {
			continue
		}
		t.out[ip] = target{p, tg.IP}
		t.in[tg.Tailnet][tg.IP] = ip
	}
	for pfx, tg := range r.plan.Subnets {
		if p := r.ports[tg.Tailnet]; p != nil {
			t.subnets = append(t.subnets, subnet{pfx, tg.Prefix, p})
		}
	}
	slices.SortFunc(t.subnets, func(a, b subnet) int { return b.routed.Bits() - a.routed.Bits() })
	r.tab.Store(t)
}

// lookupOut finds where a packet from the host to dst goes: the tailnet and the real
// address there.
func (t *tables) lookupOut(dst netip.Addr) (target, bool) {
	if tg, ok := t.out[dst]; ok {
		return tg, true
	}
	for _, s := range t.subnets {
		if s.routed.Contains(dst) {
			return target{s.port, translate(dst, s.real)}, true
		}
	}
	return target{}, false
}

// lookupIn returns the address a tailnet's src is routed at on this machine.
func (t *tables) lookupIn(tailnet string, src netip.Addr) (netip.Addr, bool) {
	if ip, ok := t.in[tailnet][src]; ok {
		return ip, true
	}
	// Overlapping routes of one tailnet resolve to the longest, as on the way out.
	for _, s := range t.subnets {
		if s.port.name == tailnet && s.real.Contains(src) {
			return translate(src, s.routed), true
		}
	}
	return netip.Addr{}, false
}

// translate returns a with its prefix replaced by to's; the host part stays.
func translate(a netip.Addr, to netip.Prefix) netip.Addr {
	if a.BitLen() != to.Addr().BitLen() {
		return netip.Addr{}
	}
	b, p := a.As16(), to.Addr().As16()
	bits := to.Bits()
	if a.Is4() {
		bits += 96 // As16 maps IPv4 into the last 4 bytes
	}
	for i := range 16 {
		n := min(max(bits-8*i, 0), 8) // bits of byte i that belong to the prefix
		mask := byte(0xff << (8 - n))
		b[i] = p[i]&mask | b[i]&^mask
	}
	if a.Is4() {
		return netip.AddrFrom16(b).Unmap()
	}
	return netip.AddrFrom16(b)
}

// Run reads packets from the host TUN until it is closed, then returns nil.
func (r *Router) Run() error {
	batch := r.host.BatchSize()
	bufs := make([][]byte, batch)
	sizes := make([]int, batch)
	for i := range bufs {
		bufs[i] = make([]byte, headroom+MTU)
	}
	var q packet.Parsed
	for {
		n, err := r.host.Read(bufs, sizes, headroom)
		if err != nil {
			if errors.Is(err, os.ErrClosed) {
				return nil
			}
			if errors.Is(err, tun.ErrTooManySegments) {
				continue
			}
			return err
		}
		for i := range n {
			r.fromHost(&q, bufs[i][headroom:headroom+sizes[i]])
		}
	}
}

// fromHost routes a packet the host sent into the tailnets. pkt is copied before it is
// queued, so the caller may reuse it.
func (r *Router) fromHost(q *packet.Parsed, pkt []byte) {
	q.Decode(pkt)
	if src := q.Src.Addr(); !src.IsValid() || src != r.hostIPFor(src) {
		return
	}
	t := r.tab.Load()
	tg, ok := t.lookupOut(q.Dst.Addr())
	if !ok || !tg.real.IsValid() {
		return
	}
	self := tg.port.selfFor(tg.real)
	if !self.IsValid() {
		return
	}
	cp := make([]byte, len(pkt))
	copy(cp, pkt)
	q.Decode(cp)
	checksum.UpdateDstAddr(q, tg.real)
	checksum.UpdateSrcAddr(q, self)
	tg.port.dev.queue(cp)
}

// hostIPFor returns the host's TUN address in the family of a, or the zero Addr.
func (r *Router) hostIPFor(a netip.Addr) netip.Addr {
	if a.Is4() {
		return r.hostIP
	}
	return r.hostIP6
}

// fromTailnet routes a packet a tailnet's engine sent to the host: answers to the host's
// connections and connections of the tailnet's peers to the host alike. It rewrites pkt
// in place.
func (r *Router) fromTailnet(p *port, pkt []byte) {
	var q packet.Parsed
	q.Decode(pkt)
	if !deliverable(&q) {
		return
	}
	t := r.tab.Load()
	cur := t.ports[p.name]
	dst := q.Dst.Addr()
	host := r.hostIPFor(dst)
	if cur == nil || cur.dev != p.dev || !dst.IsValid() || dst != cur.selfFor(dst) || !host.IsValid() {
		return
	}
	routed, ok := t.lookupIn(p.name, q.Src.Addr())
	if !ok || !routed.IsValid() {
		return
	}
	checksum.UpdateSrcAddr(&q, routed)
	checksum.UpdateDstAddr(&q, host)

	buf := make([]byte, headroom+len(pkt))
	copy(buf[headroom:], pkt)
	r.writeMu.Lock()
	_, err := r.host.Write([][]byte{buf}, headroom)
	r.writeMu.Unlock()
	if err != nil && !errors.Is(err, os.ErrClosed) {
		slog.Debug("router: write to host", "err", err)
	}
}

// deliverable reports whether the host takes a packet of q's kind from a tailnet: TCP,
// UDP and ICMP echo. Other ICMP messages would need their embedded header translated as
// well; other protocols are not supported.
func deliverable(q *packet.Parsed) bool {
	switch q.IPProto {
	case ipproto.TCP, ipproto.UDP:
		return true
	case ipproto.ICMPv4, ipproto.ICMPv6:
		return q.IsEchoRequest() || q.IsEchoResponse()
	}
	return false
}

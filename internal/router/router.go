// Package router is tailweave's userspace switch between the host and its tailnets.
//
// The host sends traffic for the tailnets into a single TUN device. The router looks up
// every packet's destination in the weave plan and hands it to the device of the
// tailnet's Tailscale engine, rewriting the destination from the IP the device is routed
// at on this machine to its real tailnet IP, and the source from the host's TUN address
// to the engine's own tailnet IP. Answers get the reverse treatment.
//
// The address translation is 1:1 and needs no state. Flows are tracked only to keep
// tailnets out: a packet from a tailnet reaches the host only when it answers a
// connection the host opened, and never another tailnet.
package router

import (
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nikiosna/tailweave/internal/weave"
	"github.com/tailscale/wireguard-go/tun"
	"tailscale.com/net/packet"
	"tailscale.com/net/packet/checksum"
)

// headroom is the space kept in front of packets written to the host TUN; Linux TUNs with
// offloads need room for a virtio header there.
const headroom = 16

// Router moves packets between the host TUN and the tailnets' devices.
type Router struct {
	host   tun.Device
	hostIP netip.Addr
	flows  *flows

	writeMu sync.Mutex // serializes writes to host

	mu    sync.Mutex // guards ports and plan, and rebuilding tables
	ports map[string]*port
	plan  weave.Plan
	tab   atomic.Pointer[tables]
}

// port is a tailnet's attachment to the router.
type port struct {
	name string
	dev  *device
	self netip.Addr // the engine's own tailnet IPv4; zero until known
}

// tables is an immutable snapshot of everything needed to route a packet.
type tables struct {
	out   map[netip.Addr]target                // routed IP -> tailnet and real IP
	in    map[string]map[netip.Addr]netip.Addr // tailnet -> real IP -> routed IP
	ports map[string]*port
}

type target struct {
	port *port
	real netip.Addr
}

// New returns a router for the host TUN whose address on the host is hostIP. Call Run to
// start moving packets.
func New(host tun.Device, hostIP netip.Addr) *Router {
	r := &Router{host: host, hostIP: hostIP, flows: newFlows(), ports: map[string]*port{}}
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

// RemoveTailnet detaches a tailnet and forgets its flows.
func (r *Router) RemoveTailnet(name string) {
	r.mu.Lock()
	if p := r.ports[name]; p != nil {
		p.dev.Close()
		delete(r.ports, name)
		r.rebuildLocked()
	}
	r.mu.Unlock()
	r.flows.expire(name)
}

// SetSelf records a tailnet's own IPv4 address, the source of everything the host sends
// into it.
func (r *Router) SetSelf(name string, ip netip.Addr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.ports[name]; p != nil && p.self != ip {
		// Ports are shared with the current snapshot, so replace instead of mutating.
		np := &port{name: name, dev: p.dev, self: ip}
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
	r.tab.Store(t)
}

// Run reads packets from the host TUN until it is closed, then returns nil.
func (r *Router) Run() error {
	batch := r.host.BatchSize()
	bufs := make([][]byte, batch)
	sizes := make([]int, batch)
	for i := range bufs {
		bufs[i] = make([]byte, headroom+MTU)
	}
	gc := time.NewTicker(time.Minute)
	defer gc.Stop()
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
		select {
		case <-gc.C:
			r.flows.expire("")
		default:
		}
	}
}

// fromHost routes a packet the host sent into the tailnets. pkt is copied before it is
// queued, so the caller may reuse it.
func (r *Router) fromHost(q *packet.Parsed, pkt []byte) {
	q.Decode(pkt)
	if q.IPVersion != 4 || q.Src.Addr() != r.hostIP {
		return
	}
	t := r.tab.Load()
	tg, ok := t.out[q.Dst.Addr()]
	if !ok || !tg.port.self.IsValid() {
		return
	}
	cp := make([]byte, len(pkt))
	copy(cp, pkt)
	q.Decode(cp)
	checksum.UpdateDstAddr(q, tg.real)
	checksum.UpdateSrcAddr(q, tg.port.self)
	if k, ok := outKey(tg.port.name, q); ok {
		r.flows.add(k)
	}
	tg.port.dev.queue(cp)
}

// fromTailnet routes a packet a tailnet's engine sent to the host. It rewrites pkt in
// place.
func (r *Router) fromTailnet(p *port, pkt []byte) {
	var q packet.Parsed
	q.Decode(pkt)
	if q.IPVersion != 4 {
		return
	}
	t := r.tab.Load()
	cur := t.ports[p.name]
	if cur == nil || cur.dev != p.dev || q.Dst.Addr() != cur.self {
		return
	}
	routed, ok := t.in[p.name][q.Src.Addr()]
	if !ok {
		return
	}
	k, ok := inKey(p.name, &q)
	if !ok || !r.flows.allowed(k) {
		return
	}
	checksum.UpdateSrcAddr(&q, routed)
	checksum.UpdateDstAddr(&q, r.hostIP)

	buf := make([]byte, headroom+len(pkt))
	copy(buf[headroom:], pkt)
	r.writeMu.Lock()
	_, err := r.host.Write([][]byte{buf}, headroom)
	r.writeMu.Unlock()
	if err != nil && !errors.Is(err, os.ErrClosed) {
		slog.Debug("router: write to host", "err", err)
	}
}

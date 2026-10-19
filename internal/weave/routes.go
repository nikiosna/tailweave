package weave

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
)

// ViaRange holds Tailscale's 4via6 addresses, fd7a:115c:a1e0:b1a:0:<site>:<IPv4>.
var ViaRange = netip.MustParsePrefix("fd7a:115c:a1e0:b1a::/64")

// IsVia reports whether p is a 4via6 route.
func IsVia(p netip.Prefix) bool {
	return p.Addr().Is6() && p.Bits() >= ViaRange.Bits() && ViaRange.Contains(p.Addr())
}

// Routable reports whether tailweave handles a subnet route: IPv4 routes other than the
// default route, and 4via6 routes. Exit routes are a separate matter.
func Routable(p netip.Prefix) bool {
	return p.IsValid() && (p.Addr().Is4() && p.Bits() > 0 || IsVia(p))
}

type routeKey struct {
	tailnet string
	route   netip.Prefix
}

func (m Mapping) routeKey() routeKey { return routeKey{m.Tailnet, m.Route.Masked()} }

// routeClaim is a tailnet's subnet route and the router it currently goes through.
type routeClaim struct {
	routeKey
	router DeviceRef
}

// owner is a prefix routed on this machine: a real route, or a mapped block (mapped).
type owner struct {
	prefix netip.Prefix
	claim  routeClaim
	mapped bool
}

// routeBlocks returns the pool blocks route mappings may keep: manual ones, and auto ones
// while auto-mapping is on.
func routeBlocks(ms []Mapping, autoMap bool) []netip.Prefix {
	var bs []netip.Prefix
	for _, m := range ms {
		if m.Prefix.Addr().Is4() && (!m.Auto || autoMap) {
			bs = append(bs, m.Prefix.Masked())
		}
	}
	return bs
}

// addRoutes routes the tailnets' subnet routes and returns them per device. used holds
// the IPs of active device mappings, which blocks must not contain.
func (p *Plan) addRoutes(tailnets []Tailnet, mappings []Mapping, opt Options, used map[netip.Addr]bool) map[peerKey][]Route {
	// Who claims which route, in priority order. A tailnet has one primary router per route.
	var claims []routeClaim
	seen := map[routeKey]bool{}
	for _, tn := range tailnets {
		for _, pe := range sortedPeers(tn.Peers) {
			for _, r := range sortedPrefixes(pe.Routes) {
				k := routeKey{tn.Name, r.Masked()}
				if !Routable(r) || seen[k] {
					continue
				}
				seen[k] = true
				claims = append(claims, routeClaim{k, DeviceRef{tn.Name, pe.ID, pe.Name}})
			}
		}
	}
	claimOf := map[routeKey]routeClaim{}
	for _, c := range claims {
		claimOf[c.routeKey] = c
	}

	// A block must not hold a device's address, a network of this machine or anything
	// already routed.
	var owners []owner
	overlapsOwner := func(b netip.Prefix) bool {
		return slices.ContainsFunc(owners, func(o owner) bool { return o.prefix.Overlaps(b) })
	}
	holdsIP := func(b netip.Prefix) bool {
		for ip := range p.real {
			if b.Contains(ip) {
				return true
			}
		}
		for ip := range used {
			if b.Contains(ip) {
				return true
			}
		}
		return false
	}
	foreign := func(b netip.Prefix) bool {
		return slices.ContainsFunc(p.exclude, func(x netip.Prefix) bool { return x.Overlaps(b) })
	}
	// Real routes must stay out of the pool, too; blocks come from it.
	local := func(r netip.Prefix) bool { return foreign(r) || p.pool.IsValid() && p.pool.Overlaps(r) }

	// Manual mappings first: they take their route out of the collision handling.
	active := map[routeKey]Mapping{}
	autoOld := map[routeKey]Mapping{}
	for _, m := range mappings {
		m.Route, m.Prefix = m.Route.Masked(), m.Prefix.Masked()
		if m.Auto {
			autoOld[m.routeKey()] = m
			continue
		}
		p.Mappings = append(p.Mappings, m)
		c, ok := claimOf[m.routeKey()]
		if !ok {
			continue // route gone; keep the mapping in case it comes back
		}
		msg := ""
		switch {
		case !m.Route.Addr().Is4() || !m.Prefix.Addr().Is4() || m.Prefix.Bits() != m.Route.Bits():
			msg = "mapped prefix must be an IPv4 prefix of the same size as the route"
		case foreign(m.Prefix):
			msg = "mapped prefix overlaps a network of this machine"
		case holdsIP(m.Prefix):
			msg = "mapped prefix contains an IP used by a device"
		case slices.ContainsFunc(claims, func(c routeClaim) bool { return c.route.Overlaps(m.Prefix) }):
			msg = "mapped prefix overlaps a route of a tailnet"
		case overlapsOwner(m.Prefix):
			msg = "mapped prefix overlaps another mapping"
		}
		if msg != "" {
			p.Conflicts = append(p.Conflicts, Conflict{Kind: ConflictMapping, Key: m.Prefix.String(),
				Devices: []DeviceRef{c.router}, Message: msg})
			continue
		}
		active[m.routeKey()] = m
		owners = append(owners, owner{m.Prefix, c, true})
	}

	// The first claim of a prefix wins; a claim that overlaps another tailnet's route or a
	// mapped block loses, and so does one that overlaps a network of this machine.
	conflicts := map[netip.Prefix]*Conflict{}
	var order []netip.Prefix
	type loser struct {
		routeClaim
		conflict netip.Prefix
	}
	var losers []loser
	for _, c := range claims {
		if _, mapped := active[c.routeKey]; mapped {
			continue
		}
		if c.route.Addr().Is4() && local(c.route) {
			conflicts[c.route] = &Conflict{Kind: ConflictLocal, Key: c.route.String(), Devices: []DeviceRef{c.router}}
			order = append(order, c.route)
			losers = append(losers, loser{c, c.route})
			continue
		}
		i := slices.IndexFunc(owners, func(o owner) bool {
			return o.prefix.Overlaps(c.route) && (o.mapped || o.claim.tailnet != c.tailnet)
		})
		if i < 0 {
			owners = append(owners, owner{c.route, c, false})
			p.Subnets[c.route] = SubnetTarget{c.tailnet, c.route}
			continue
		}
		o := owners[i]
		cf := conflicts[o.prefix]
		if cf == nil {
			cf = &Conflict{Kind: ConflictRoute, Key: o.prefix.String(), Devices: []DeviceRef{o.claim.router}}
			conflicts[o.prefix] = cf
			order = append(order, o.prefix)
		}
		cf.Devices = append(cf.Devices, c.router)
		losers = append(losers, loser{c, o.prefix})
	}

	// Old auto-mappings may only be reused if their block is still free.
	for k, m := range autoOld {
		if !opt.AutoMap || !m.Prefix.Addr().Is4() || m.Prefix.Bits() != m.Route.Bits() ||
			foreign(m.Prefix) || holdsIP(m.Prefix) || overlapsOwner(m.Prefix) {
			delete(autoOld, k)
		}
	}
	reserved := map[routeKey]netip.Prefix{}
	for k, m := range autoOld {
		reserved[k] = m.Prefix
	}

	// Losers get a block from the pool; 4via6 routes stay unrouted.
	failed := map[netip.Prefix]bool{}    // conflicts with a route left unrouted
	exhausted := map[netip.Prefix]bool{} // ... because the pool is full
	for _, c := range losers {
		if IsVia(c.route) || !opt.AutoMap {
			failed[c.conflict] = true
			continue
		}
		m, ok := autoOld[c.routeKey]
		if !ok {
			b := nextFreeBlock(opt.Pool, c.route.Bits(), func(b netip.Prefix) bool {
				if foreign(b) || holdsIP(b) || overlapsOwner(b) {
					return true
				}
				for k, r := range reserved {
					if k != c.routeKey && r.Overlaps(b) {
						return true
					}
				}
				return false
			})
			if !b.IsValid() {
				failed[c.conflict], exhausted[c.conflict] = true, true
				continue
			}
			m = Mapping{Tailnet: c.tailnet, Route: c.route, Prefix: b, Auto: true}
		}
		delete(reserved, c.routeKey)
		active[c.routeKey] = m
		owners = append(owners, owner{m.Prefix, c.routeClaim, true})
		p.Mappings = append(p.Mappings, m)
	}

	for _, key := range order {
		cf := conflicts[key]
		cf.Resolved = !failed[key]
		switch {
		case exhausted[key]:
			cf.Message = "address pool exhausted, some routes are not routed"
		case cf.Kind == ConflictLocal && cf.Resolved:
			cf.Message = "overlaps a network of this machine, remapped"
		case cf.Kind == ConflictLocal:
			cf.Message = "not routed: overlaps a network of this machine"
		case cf.Resolved:
			cf.Message = fmt.Sprintf("%s keeps the route, the others were remapped", cf.Devices[0].Tailnet)
		default:
			cf.Message = "not routed: route overlaps one in another tailnet"
		}
		p.Conflicts = append(p.Conflicts, *cf)
	}

	for k, m := range active {
		p.Subnets[m.Prefix] = SubnetTarget{k.tailnet, k.route}
	}

	// 4via6 names resolve in the domain of the route's tailnet.
	domains := map[string]string{}
	for _, tn := range tailnets {
		domains[tn.Name] = normDomain(tn.Domain)
	}
	for s, t := range p.Subnets {
		if d := domains[t.Tailnet]; IsVia(s) && d != "" {
			p.Via[d] = append(p.Via[d], s)
		}
	}
	for d := range p.Via {
		slices.SortFunc(p.Via[d], comparePrefix)
	}

	routes := map[peerKey][]Route{}
	for _, c := range claims {
		r := Route{Prefix: c.route}
		if m, ok := active[c.routeKey]; ok {
			r.Routed = m.Prefix
			r.Mapping = &m
		} else if t, ok := p.Subnets[c.route]; ok && t.Tailnet == c.tailnet {
			r.Routed = c.route
		}
		k := peerKey{c.tailnet, c.router.PeerID}
		routes[k] = append(routes[k], r)
	}
	return routes
}

// FreePrefix reports whether b can be used for a new route mapping.
func (p Plan) FreePrefix(b netip.Prefix) bool {
	if !b.Addr().Is4() {
		return false
	}
	for ip := range p.real {
		if b.Contains(ip) {
			return false
		}
	}
	for ip := range p.Routes {
		if b.Contains(ip) {
			return false
		}
	}
	for s := range p.Subnets {
		if s.Overlaps(b) {
			return false
		}
	}
	for _, m := range p.Mappings {
		if b.Contains(m.IP) || m.Prefix.IsValid() && m.Prefix.Overlaps(b) {
			return false
		}
	}
	return !slices.ContainsFunc(p.exclude, func(x netip.Prefix) bool { return x.Overlaps(b) })
}

// SuggestPrefix returns a free pool block with bits prefix length, or the zero Prefix.
func (p Plan) SuggestPrefix(pool netip.Prefix, bits int) netip.Prefix {
	return nextFreeBlock(pool, bits, func(b netip.Prefix) bool { return !p.FreePrefix(b) })
}

// nextFreeBlock returns the first aligned block of the given size in pool that is not taken.
func nextFreeBlock(pool netip.Prefix, bits int, taken func(netip.Prefix) bool) netip.Prefix {
	if !pool.IsValid() || !pool.Addr().Is4() || bits < pool.Bits() || bits > 32 {
		return netip.Prefix{}
	}
	pool = pool.Masked()
	for a := pool.Addr(); pool.Contains(a); {
		b := netip.PrefixFrom(a, bits)
		if !taken(b) {
			return b
		}
		next := addUint(a, uint32(1)<<(32-bits))
		if !next.IsValid() {
			break
		}
		a = next
	}
	return netip.Prefix{}
}

// addUint adds n to an IPv4 address; it returns the zero Addr on overflow.
func addUint(a netip.Addr, n uint32) netip.Addr {
	b := a.As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	if v+n < v {
		return netip.Addr{}
	}
	v += n
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

func sortedPrefixes(ps []netip.Prefix) []netip.Prefix {
	s := slices.Clone(ps)
	slices.SortFunc(s, comparePrefix)
	return s
}

func comparePrefix(a, b netip.Prefix) int {
	return cmp.Or(a.Addr().Compare(b.Addr()), cmp.Compare(a.Bits(), b.Bits()))
}

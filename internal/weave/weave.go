// Package weave merges several tailnets into one address space.
//
// Every peer keeps its real tailnet IP and is routed into the network namespace of its
// tailnet. When the same IP exists in several tailnets, the collision is either resolved
// automatically (the tailnet listed first keeps the IP, the others get an address from a
// pool) or left unrouted until the user maps a device by hand. MagicDNS names always
// resolve to the IP that is actually routed for a device.
//
// Subnet routes follow the same model per prefix: the tailnet listed first keeps a
// route, overlapping routes of other tailnets get a block of the same size from the pool
// (or stay unrouted). Routes that overlap a network of this machine are never routed as
// they are. 4via6 routes are unique by design and are never remapped.
//
// The package is pure: it computes a Plan from the tailnets' peers, the stored mappings
// and the options. Applying the plan is up to the caller.
package weave

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// Peer is a device in a tailnet.
type Peer struct {
	ID      string // stable node ID within the tailnet
	Name    string // host name
	DNSName string // MagicDNS name, e.g. "peer-a1.a.lab."
	IPs     []netip.Addr
	Routes  []netip.Prefix // subnet routes this peer is the primary router for
	Online  bool
}

// IPv4 returns the peer's tailnet IPv4 address, or the zero Addr.
func (p Peer) IPv4() netip.Addr {
	for _, a := range p.IPs {
		if a.Is4() {
			return a
		}
	}
	return netip.Addr{}
}

// Tailnet is the current view of one tailnet, as seen by its instance on this machine.
type Tailnet struct {
	Name        string // tailweave's name for it, e.g. "a"
	Domain      string // MagicDNS suffix, e.g. "a.lab"; empty if MagicDNS is off
	Self        Peer
	PeerAPIPort uint16 // port of the instance's own PeerAPI on its IP; 0 if unknown
	Peers       []Peer
}

// Mapping gives a device an IP on this machine other than its real tailnet IP, or a
// subnet route (Route) a prefix of the same size (Prefix). Route mappings belong to the
// tailnet, not to a router, so they survive a failover to another subnet router.
type Mapping struct {
	Tailnet string       `json:"tailnet"`
	PeerID  string       `json:"peer,omitempty"`
	IP      netip.Addr   `json:"ip,omitzero"`
	Route   netip.Prefix `json:"route,omitzero"`
	Prefix  netip.Prefix `json:"prefix,omitzero"`
	Auto    bool         `json:"auto,omitempty"` // created by auto-remapping, not by the user
}

func (m Mapping) key() peerKey { return peerKey{m.Tailnet, m.PeerID} }

// Options control how collisions are handled.
type Options struct {
	AutoMap bool         // remap colliding devices and routes automatically
	Pool    netip.Prefix // addresses for auto-mappings and suggestions
	// Exclude are networks of this machine (LAN, tailweave's own ranges) that subnet
	// routes must not take over. The pool is always excluded.
	Exclude []netip.Prefix
}

// Target is where traffic to a routed IP goes: a tailnet and the device's real IP there.
type Target struct {
	Tailnet string
	IP      netip.Addr
}

// Device is a peer together with what this machine does with it.
type Device struct {
	Tailnet  string     `json:"tailnet"`
	PeerID   string     `json:"peer"`
	Name     string     `json:"name"`
	DNSName  string     `json:"dnsName"`
	Online   bool       `json:"online"`
	RealIP   netip.Addr `json:"realIP"`
	RoutedIP netip.Addr `json:"routedIP"` // zero if the device is not reachable from this machine
	Mapping  *Mapping   `json:"mapping,omitempty"`
	Routes   []Route    `json:"routes,omitempty"` // subnet routes of the device
}

// Route is a subnet route of a device and where it is reachable on this machine.
type Route struct {
	Prefix  netip.Prefix `json:"prefix"`
	Routed  netip.Prefix `json:"routed,omitzero"` // zero if the route is not reachable
	Mapping *Mapping     `json:"mapping,omitempty"`
}

// SubnetTarget is where traffic to a routed prefix goes: a tailnet and the real route there.
type SubnetTarget struct {
	Tailnet string
	Prefix  netip.Prefix
}

// Conflict kinds.
const (
	ConflictIP      = "ip"      // same IP in several tailnets
	ConflictName    = "name"    // same MagicDNS name in several tailnets
	ConflictMapping = "mapping" // a stored mapping cannot be applied
	ConflictRoute   = "route"   // overlapping subnet routes in several tailnets
	ConflictLocal   = "local"   // a subnet route overlaps a network of this machine
)

// DeviceRef identifies a device in a conflict.
type DeviceRef struct {
	Tailnet string `json:"tailnet"`
	PeerID  string `json:"peer"`
	Name    string `json:"name"`
}

// Conflict is something the user may want to look at.
type Conflict struct {
	Kind     string      `json:"kind"`
	Key      string      `json:"key"` // the IP, name or prefix concerned
	Devices  []DeviceRef `json:"devices"`
	Resolved bool        `json:"resolved"` // handled automatically (auto-mapping, priority)
	Message  string      `json:"message"`
}

// Local is this machine's own address in a tailnet.
type Local struct {
	IP          netip.Addr // the instance's tailnet IPv4
	PeerAPIPort uint16     // see Tailnet
}

// Plan is the desired state for this machine.
type Plan struct {
	Routes    map[netip.Addr]Target         // IP on this machine -> tailnet and real IP
	Subnets   map[netip.Prefix]SubnetTarget // prefix on this machine -> tailnet and real route
	Via       map[string][]netip.Prefix     // MagicDNS suffix -> routed 4via6 prefixes
	Local     map[string]Local              // tailnet -> this machine's address there
	Names     map[string]netip.Addr         // lowercase FQDN with trailing dot -> routed IP
	Domains   []string                      // MagicDNS suffixes of all tailnets, deduplicated
	Devices   []Device                      // all peers, in tailnet priority order and by IP
	Mappings  []Mapping                     // mappings to store: manual ones plus active auto ones
	Conflicts []Conflict

	real    map[netip.Addr]bool // every IPv4 used by a tailnet, including self IPs
	exclude []netip.Prefix      // see Options
	pool    netip.Prefix
}

type peerKey struct{ tailnet, id string }

// Compute builds the plan. Tailnets are given in priority order: on collisions, the
// tailnet listed first keeps the IP or the name.
func Compute(tailnets []Tailnet, mappings []Mapping, opt Options) Plan {
	real := map[netip.Addr]bool{}
	p := Plan{Routes: map[netip.Addr]Target{}, Subnets: map[netip.Prefix]SubnetTarget{},
		Via: map[string][]netip.Prefix{}, Local: map[string]Local{}, Names: map[string]netip.Addr{}, real: real,
		exclude: opt.Exclude, pool: opt.Pool.Masked()}

	peers := map[peerKey]Peer{}
	for _, tn := range tailnets {
		if a := tn.Self.IPv4(); a.IsValid() {
			real[a] = true
			p.Local[tn.Name] = Local{IP: a, PeerAPIPort: tn.PeerAPIPort}
		}
		for _, pe := range tn.Peers {
			if a := pe.IPv4(); a.IsValid() {
				peers[peerKey{tn.Name, pe.ID}] = pe
				real[a] = true
			}
		}
		if d := normDomain(tn.Domain); d != "" && !slices.Contains(p.Domains, d) {
			p.Domains = append(p.Domains, d)
		}
	}

	// Manual mappings first: they take their device out of the collision handling.
	active := map[peerKey]Mapping{}
	used := map[netip.Addr]bool{} // mapped IPs in use
	autoOld := map[peerKey]Mapping{}
	var routeMaps []Mapping
	for _, m := range mappings {
		if m.Route.IsValid() {
			routeMaps = append(routeMaps, m)
			continue
		}
		if m.Auto {
			autoOld[m.key()] = m
			continue
		}
		p.Mappings = append(p.Mappings, m)
		pe, ok := peers[m.key()]
		if !ok {
			continue // device gone; keep the mapping in case it comes back
		}
		ref := DeviceRef{m.Tailnet, m.PeerID, pe.Name}
		switch {
		case !m.IP.Is4():
			p.Conflicts = append(p.Conflicts, Conflict{Kind: ConflictMapping, Key: m.IP.String(),
				Devices: []DeviceRef{ref}, Message: "mapped IP is not an IPv4 address"})
		case real[m.IP]:
			p.Conflicts = append(p.Conflicts, Conflict{Kind: ConflictMapping, Key: m.IP.String(),
				Devices: []DeviceRef{ref}, Message: "mapped IP is used by a tailnet"})
		case used[m.IP]:
			p.Conflicts = append(p.Conflicts, Conflict{Kind: ConflictMapping, Key: m.IP.String(),
				Devices: []DeviceRef{ref}, Message: "mapped IP is already used by another mapping"})
		default:
			active[m.key()] = m
			used[m.IP] = true
		}
	}

	// Who claims which real IP.
	claims := map[netip.Addr][]peerKey{}
	var order []netip.Addr
	for _, tn := range tailnets {
		for _, pe := range sortedPeers(tn.Peers) {
			k := peerKey{tn.Name, pe.ID}
			a := pe.IPv4()
			if !a.IsValid() {
				continue
			}
			if _, mapped := active[k]; mapped {
				continue
			}
			if claims[a] == nil {
				order = append(order, a)
			}
			claims[a] = append(claims[a], k)
		}
	}

	// Old auto-mappings may only be reused if their IP is still free.
	for _, m := range autoOld {
		if !m.IP.Is4() || real[m.IP] || used[m.IP] || !opt.AutoMap {
			delete(autoOld, m.key())
		}
	}
	reserved := map[netip.Addr]bool{}
	for _, m := range autoOld {
		reserved[m.IP] = true
	}
	// Pool blocks that route mappings may keep are off limits for devices.
	blocks := routeBlocks(routeMaps, opt.AutoMap)
	inBlock := func(ip netip.Addr) bool {
		return slices.ContainsFunc(blocks, func(b netip.Prefix) bool { return b.Contains(ip) })
	}

	var autoNew []Mapping
	for _, a := range order {
		ks := claims[a]
		if len(ks) == 1 {
			p.Routes[a] = Target{ks[0].tailnet, a}
			continue
		}
		c := Conflict{Kind: ConflictIP, Key: a.String()}
		for _, k := range ks {
			c.Devices = append(c.Devices, DeviceRef{k.tailnet, k.id, peers[k].Name})
		}
		if !opt.AutoMap {
			c.Message = "not routed: IP exists in several tailnets"
			p.Conflicts = append(p.Conflicts, c)
			continue
		}
		p.Routes[a] = Target{ks[0].tailnet, a}
		c.Resolved = true
		c.Message = fmt.Sprintf("%s keeps the IP, the others were remapped", ks[0].tailnet)
		for _, k := range ks[1:] {
			m, ok := autoOld[k]
			if !ok {
				ip := nextFree(opt.Pool, func(ip netip.Addr) bool { return real[ip] || used[ip] || reserved[ip] || inBlock(ip) })
				if !ip.IsValid() {
					c.Resolved = false
					c.Message = "address pool exhausted, some devices are not routed"
					continue
				}
				m = Mapping{Tailnet: k.tailnet, PeerID: k.id, IP: ip, Auto: true}
			}
			active[k] = m
			used[m.IP] = true
			autoNew = append(autoNew, m)
		}
		p.Conflicts = append(p.Conflicts, c)
	}
	p.Mappings = append(p.Mappings, autoNew...)

	for k, m := range active {
		p.Routes[m.IP] = Target{k.tailnet, peers[k].IPv4()}
	}

	routes := p.addRoutes(tailnets, routeMaps, opt, used)

	// Devices and names, in priority order.
	nameOwner := map[string]peerKey{}
	nameConflicts := map[string]*Conflict{}
	var nameOrder []string
	for _, tn := range tailnets {
		for _, pe := range sortedPeers(tn.Peers) {
			k := peerKey{tn.Name, pe.ID}
			d := Device{Tailnet: tn.Name, PeerID: pe.ID, Name: pe.Name, DNSName: pe.DNSName,
				Online: pe.Online, RealIP: pe.IPv4()}
			if m, ok := active[k]; ok {
				d.RoutedIP = m.IP
				d.Mapping = &m
			} else if t, ok := p.Routes[d.RealIP]; ok && t.Tailnet == tn.Name {
				d.RoutedIP = d.RealIP
			}
			d.Routes = routes[k]
			p.Devices = append(p.Devices, d)

			name := normName(pe.DNSName)
			if name == "" || !d.RoutedIP.IsValid() {
				continue
			}
			if owner, taken := nameOwner[name]; taken {
				c := nameConflicts[name]
				if c == nil {
					c = &Conflict{Kind: ConflictName, Key: name, Resolved: true,
						Devices: []DeviceRef{{owner.tailnet, owner.id, peers[owner].Name}},
						Message: fmt.Sprintf("name resolves to the device in %s", owner.tailnet)}
					nameConflicts[name] = c
					nameOrder = append(nameOrder, name)
				}
				c.Devices = append(c.Devices, DeviceRef{tn.Name, pe.ID, pe.Name})
				continue
			}
			nameOwner[name] = k
			p.Names[name] = d.RoutedIP
		}
	}
	for _, n := range nameOrder {
		p.Conflicts = append(p.Conflicts, *nameConflicts[n])
	}
	return p
}

// Suggest returns a free pool address for a new mapping, or the zero Addr.
func (p Plan) Suggest(pool netip.Prefix) netip.Addr {
	return nextFree(pool, func(ip netip.Addr) bool { return !p.Free(ip) })
}

// Free reports whether ip can be used for a new mapping.
func (p Plan) Free(ip netip.Addr) bool {
	if !ip.Is4() || p.real[ip] {
		return false
	}
	if _, routed := p.Routes[ip]; routed {
		return false
	}
	for _, m := range p.Mappings {
		if m.IP == ip || m.Prefix.Contains(ip) {
			return false
		}
	}
	for s := range p.Subnets {
		if s.Contains(ip) {
			return false
		}
	}
	return true
}

func nextFree(pool netip.Prefix, taken func(netip.Addr) bool) netip.Addr {
	if !pool.IsValid() || !pool.Addr().Is4() {
		return netip.Addr{}
	}
	pool = pool.Masked()
	// Skip the network address; the broadcast address is skipped by the bound check.
	for ip := pool.Addr().Next(); pool.Contains(ip) && pool.Contains(ip.Next()); ip = ip.Next() {
		if !taken(ip) {
			return ip
		}
	}
	return netip.Addr{}
}

func sortedPeers(peers []Peer) []Peer {
	s := slices.Clone(peers)
	slices.SortStableFunc(s, func(a, b Peer) int {
		return cmp.Or(a.IPv4().Compare(b.IPv4()), strings.Compare(a.ID, b.ID))
	})
	return s
}

func normName(n string) string {
	n = strings.ToLower(strings.TrimSpace(n))
	if n == "" || n == "." {
		return ""
	}
	if !strings.HasSuffix(n, ".") {
		n += "."
	}
	return n
}

func normDomain(d string) string {
	return strings.TrimSuffix(normName(d), ".")
}

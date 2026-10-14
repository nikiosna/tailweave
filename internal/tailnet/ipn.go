package tailnet

import "tailscale.com/ipn/ipnstate"

// Login is how an instance logs in to its tailnet.
type Login struct {
	LoginServer string
	Hostname    string
	AuthKey     string // empty: interactive login, the URL shows up in the status
}

// FromIPN converts the status of an in-process Tailscale engine, the same way Status
// converts tailscaled's.
func FromIPN(name string, st *ipnstate.Status) Status {
	s := status{BackendState: st.BackendState, AuthURL: st.AuthURL, MagicDNSSuffix: st.MagicDNSSuffix}
	if st.CurrentTailnet != nil {
		s.CurrentTailnet = &struct{ MagicDNSEnabled bool }{st.CurrentTailnet.MagicDNSEnabled}
	}
	if st.Self != nil {
		s.Self = fromPeerStatus(st.Self)
	}
	s.Peer = map[string]*node{}
	for k, p := range st.Peer {
		s.Peer[k.String()] = fromPeerStatus(p)
	}
	return convert(name, s)
}

func fromPeerStatus(p *ipnstate.PeerStatus) *node {
	return &node{ID: string(p.ID), HostName: p.HostName, DNSName: p.DNSName,
		TailscaleIPs: p.TailscaleIPs, Online: p.Online}
}

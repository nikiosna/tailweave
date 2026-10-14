//go:build lab && linux

package daemon_test

import (
	"net/netip"
	"os"
	"testing"

	"github.com/Nikiosna/tailweave/internal/daemon"
	"github.com/Nikiosna/tailweave/internal/labtest"
)

// TestIsolation checks that only the host itself reaches the tailnets: neither a device
// on the host's LAN that routes the tailnet ranges via the host, nor one tailnet's
// namespace, nor a peer in a tailnet gets into a tailnet or the host through tailweave.
func TestIsolation(t *testing.T) {
	labtest.RequireLab(t)
	tw := startTailweave(t, t.TempDir())
	tw.login(t, labtest.TailnetA)
	tw.login(t, labtest.TailnetB)
	st := waitState(t, tw, func(s daemon.State) bool { return device(s, "b", "peer-b3") != nil })
	a1 := labtest.NodeIP(t, labtest.TailnetA, "peer-a1")
	b1 := device(st, "b", "peer-b1").RoutedIP
	b3 := device(st, "b", "peer-b3").RealIP
	if got := curl(t, b3.String()); got != "peer-b3" {
		t.Fatalf("host does not reach peer-b3: %q", got)
	}

	t.Run("lan", func(t *testing.T) {
		lan := lanClient(t)
		for _, ip := range []netip.Addr{a1, b1, b3} {
			if out, err := labtest.Output("sudo", "-n", "ip", "netns", "exec", lan, "curl", "-s", "-m3", "http://"+ip.String()+"/"); err == nil {
				t.Errorf("LAN client reaches %s: %q", ip, out)
			}
		}
	})

	t.Run("peers", func(t *testing.T) {
		// The host listens on SSH; a peer must not reach it via this machine's tailnet IP.
		// (Pings are no test: with the netns backend, the namespace itself answers them.)
		self := st.Tailnets[0].IP.String()
		if out, err := labtest.Output("docker", "exec", "peer-a1", "nc", "-z", "-w2", self, "22"); err == nil {
			t.Errorf("peer-a1 reaches the host's SSH at %s: %s", self, out)
		}
	})

	t.Run("namespaces", func(t *testing.T) {
		if os.Getenv("TAILWEAVE_BACKEND") == "tsnet" {
			t.Skip("the tsnet backend has no namespaces")
		}
		for _, ip := range []netip.Addr{b1, b3} {
			if out, err := labtest.Output("sudo", "-n", "ip", "netns", "exec", "tailweave-a", "curl", "-s", "-m3", "http://"+ip.String()+"/"); err == nil {
				t.Errorf("namespace of tailnet a reaches %s: %q", ip, out)
			}
		}
	})
}

// lanClient creates a namespace that plays a device on the host's LAN with a route to
// everything via the host, and returns its name. FORWARD is opened for it so Docker's
// DROP policy in the VM does not hide what a host without Docker would forward.
func lanClient(t *testing.T) string {
	t.Helper()
	const ns = "twtest-lan"
	sudo := func(args ...string) { t.Helper(); labtest.Run(t, "sudo", append([]string{"-n"}, args...)...) }
	cleanup := func() {
		_, _ = labtest.Output("sudo", "-n", "ip", "netns", "del", ns)
		for _, dir := range []string{"-i", "-o"} {
			for {
				if _, err := labtest.Output("sudo", "-n", "iptables", "-D", "DOCKER-USER", dir, "lanh0", "-j", "ACCEPT"); err != nil {
					break
				}
			}
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	sudo("ip", "netns", "add", ns)
	sudo("ip", "link", "add", "lanh0", "type", "veth", "peer", "name", "lann0", "netns", ns)
	sudo("ip", "addr", "add", "10.202.0.1/30", "dev", "lanh0")
	sudo("ip", "link", "set", "lanh0", "up")
	sudo("ip", "-n", ns, "addr", "add", "10.202.0.2/30", "dev", "lann0")
	sudo("ip", "-n", ns, "link", "set", "lann0", "up")
	sudo("ip", "-n", ns, "route", "add", "default", "via", "10.202.0.1")
	if _, err := labtest.Output("sudo", "-n", "iptables", "-n", "-L", "DOCKER-USER"); err == nil {
		sudo("iptables", "-I", "DOCKER-USER", "-i", "lanh0", "-j", "ACCEPT")
		sudo("iptables", "-I", "DOCKER-USER", "-o", "lanh0", "-j", "ACCEPT")
	}
	return ns
}

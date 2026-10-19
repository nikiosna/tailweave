//go:build lab && linux

package daemon_test

import (
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nikiosna/tailweave/internal/daemon"
	"github.com/Nikiosna/tailweave/internal/labtest"
)

// TestIsolation checks that only the host itself reaches the tailnets: neither a device
// on the host's LAN that routes the tailnet ranges via the host, nor one tailnet's
// namespace, nor a peer in a tailnet gets into a tailnet through tailweave. Peers do reach
// the host itself, but nothing beyond it.
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
		ips := []netip.Addr{a1, b1, b3}
		if s := labtest.Scenario(); s == "subnets" || s == "all" {
			// Subnet routes, as they are and remapped.
			st := waitState(t, tw, func(s daemon.State) bool { return routeOf(s, "b", labSubnet).Routed.IsValid() })
			ips = append(ips, netip.MustParseAddr("10.99.0.1"), hostIn(routeOf(st, "b", labSubnet).Routed, 1))
			if got := waitCurl(t, "10.99.0.1", "router-a"); got != "router-a" {
				t.Fatalf("host does not reach router-a's subnet: %q", got)
			}
		}
		for _, ip := range ips {
			if out, err := labtest.Output("sudo", "-n", "ip", "netns", "exec", lan, "curl", "-s", "-m3", "http://"+ip.String()+"/"); err == nil {
				t.Errorf("LAN client reaches %s: %q", ip, out)
			}
		}
	})

	t.Run("peers", func(t *testing.T) {
		// A peer reaches the host through this machine's IP in its tailnet, like with
		// Tailscale (TestInbound)...
		selfA := st.Tailnets[0].IP
		port, _ := remoteAddrServers(t)
		if got := peerWget(t, "peer-a1", selfA, port); got != a1.String() {
			t.Fatalf("peer-a1 -> host at %s:%d: %q, want %s", selfA, port, got, a1)
		}
		// ...but nothing behind it, not even when it routes other addresses via this
		// machine: neither another tailnet's peers, nor the host's other addresses, nor
		// the LAN.
		lan := lanClient(t)
		const lanIP, lanPort = "10.202.0.2", 8099
		resp := filepath.Join(t.TempDir(), "lan.sh")
		if err := os.WriteFile(resp, []byte("#!/bin/sh\nprintf 'HTTP/1.0 200 OK\\r\\n\\r\\nlan\\n'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		srv := exec.Command("sudo", "-n", "ip", "netns", "exec", lan, "socat",
			fmt.Sprintf("TCP-LISTEN:%d,bind=%s,fork,reuseaddr", lanPort, lanIP), "EXEC:"+resp)
		if err := srv.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = labtest.Output("sudo", "-n", "pkill", "-f", "TCP-LISTEN:"+fmt.Sprint(lanPort))
			_ = srv.Wait()
		})
		lanURL := fmt.Sprintf("http://%s:%d/", lanIP, lanPort)
		for i := 0; ; i++ {
			if _, err := labtest.Output("curl", "-sf", "-m1", lanURL); err == nil {
				break
			} else if i == 20 {
				t.Fatalf("LAN service at %s does not answer the host: %v", lanURL, err)
			}
			time.Sleep(250 * time.Millisecond)
		}
		for _, tc := range []struct {
			ip   netip.Addr
			port int
		}{
			{b1, 80}, {b3, 80}, // the peers' web servers
			{netip.MustParseAddr("10.201.0.1"), port}, {netip.MustParseAddr("10.201.0.5"), port}, // transit
			{netip.MustParseAddr(lanIP), lanPort},
		} {
			peerRoute(t, "peer-a1", tc.ip)
			if got := peerWget(t, "peer-a1", tc.ip, tc.port); got != "" {
				t.Errorf("peer-a1 reaches %s:%d via this machine: %q", tc.ip, tc.port, got)
			}
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

// peerWget fetches http://ip:port/ from inside a lab peer and returns the body, or "".
func peerWget(t *testing.T, peer string, ip netip.Addr, port int) string {
	t.Helper()
	out, err := labtest.Output("docker", "exec", peer, "wget", "-qO-", "-T3", fmt.Sprintf("http://%s:%d/", ip, port))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// peerRoute makes a lab peer send traffic for ip into its tailnet, via whatever node
// claims it there (here: none, so the tailnet's own filtering applies too), until the
// test ends.
func peerRoute(t *testing.T, peer string, ip netip.Addr) {
	t.Helper()
	dst := netip.PrefixFrom(ip, 32).String()
	labtest.Run(t, "docker", "exec", peer, "ip", "route", "replace", dst, "dev", "tailscale0", "table", "52")
	t.Cleanup(func() {
		_, _ = labtest.Output("docker", "exec", peer, "ip", "route", "del", dst, "dev", "tailscale0", "table", "52")
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

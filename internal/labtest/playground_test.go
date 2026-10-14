//go:build lab && linux

package labtest

import (
	"testing"
)

// TestPlaygroundIsolation checks the premise of tailweave: two tailscaled instances in
// separate network namespaces reach their own tailnet, even when both tailnets use the
// same addresses.
func TestPlaygroundIsolation(t *testing.T) {
	RequireLab(t)

	a1, b1 := NodeIP(t, TailnetA, "peer-a1"), NodeIP(t, TailnetB, "peer-b1")
	if a1 != b1 {
		t.Fatalf("lab should give peer-a1 and peer-b1 the same IP, got %v and %v", a1, b1)
	}

	Playground(t)

	for _, tc := range []struct{ tailnet, want string }{
		{TailnetA, "peer-a1"},
		{TailnetB, "peer-b1"},
	} {
		t.Run(tc.tailnet, func(t *testing.T) {
			ip := a1.String()
			if got := InNS(t, tc.tailnet, "curl", "-s", "-m5", "http://"+ip+"/"); got != tc.want {
				t.Errorf("tcp %s: got %q, want %q", ip, got, tc.want)
			}
			if got := InNS(t, tc.tailnet, "sh", "-c", "echo x | nc -u -w2 "+ip+" 8"); got != tc.want {
				t.Errorf("udp %s: got %q, want %q", ip, got, tc.want)
			}
		})
	}
}

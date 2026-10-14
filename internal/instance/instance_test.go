package instance

import (
	"net/netip"
	"testing"
)

func TestValidName(t *testing.T) {
	for _, n := range []string{"a", "work", "home-lab", "0x", "abcdefghijklmnopqrstuvwxyz012345"} {
		if err := ValidName(n); err != nil {
			t.Errorf("ValidName(%q) = %v", n, err)
		}
	}
	for _, n := range []string{"", "-a", "A", "a_b", "a/b", "../x", "abcdefghijklmnopqrstuvwxyz0123456"} {
		if ValidName(n) == nil {
			t.Errorf("ValidName(%q) accepted", n)
		}
	}
}

func TestAddrs(t *testing.T) {
	transit := netip.MustParsePrefix("10.201.0.0/16")
	for _, tc := range []struct {
		index    int
		host, ns string
	}{
		{0, "10.201.0.1", "10.201.0.2"},
		{1, "10.201.0.5", "10.201.0.6"},
		{64, "10.201.1.1", "10.201.1.2"},
		{16383, "10.201.255.253", "10.201.255.254"},
	} {
		host, ns, err := Addrs(transit, tc.index)
		if err != nil || host.String() != tc.host || ns.String() != tc.ns {
			t.Errorf("Addrs(%d) = %s, %s, %v; want %s, %s", tc.index, host, ns, err, tc.host, tc.ns)
		}
	}
	if _, _, err := Addrs(transit, 16384); err == nil {
		t.Error("index beyond the range accepted")
	}
	if _, _, err := Addrs(netip.MustParsePrefix("10.0.0.0/31"), 0); err == nil {
		t.Error("range smaller than /30 accepted")
	}
}

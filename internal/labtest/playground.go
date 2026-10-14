package labtest

import "testing"

const playground = "lab/scripts/netns-playground.sh"

// Playground starts one plain tailscaled per tailnet in network namespaces
// tw-a and tw-b, and tears them down when the test ends.
func Playground(t testing.TB) {
	t.Helper()
	Run(t, playground, "up")
	t.Cleanup(func() { _, _ = Output(playground, "down") })
}

// InNS runs a command inside the playground namespace of a tailnet.
func InNS(t testing.TB, tailnet string, cmd ...string) string {
	t.Helper()
	return Run(t, playground, append([]string{"exec", tailnet}, cmd...)...)
}

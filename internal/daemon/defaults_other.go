//go:build !linux

package daemon

// DefaultSocket is where the daemon serves its API unless configured otherwise.
const DefaultSocket = "/var/run/tailweave/tailweave.sock"

const defaultStateDir = "/Library/Application Support/tailweave"

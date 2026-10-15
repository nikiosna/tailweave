//go:build !linux && !darwin

package preflight

// Collect returns an empty snapshot: tailweave does not run on this platform yet.
func Collect() (Snapshot, error) { return Snapshot{}, nil }

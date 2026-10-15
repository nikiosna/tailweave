//go:build !linux && !darwin

package hostnet

import (
	"errors"
	"net/netip"
	"runtime"
)

// TunName is the name tailweave asks for when it creates its TUN device.
const TunName = "tailweave"

var errUnsupported = errors.New("tailweave does not support " + runtime.GOOS + " yet")

func Cleanup(dns netip.Addr)                      {}
func (h *Host) Setup() error                      { return errUnsupported }
func (h *Host) setDomains(domains []string) error { return errUnsupported }
func (h *Host) Teardown()                         {}

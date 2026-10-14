//go:build !linux && !darwin

package hostnet

import (
	"errors"
	"runtime"
)

// TunName is the name tailweave asks for when it creates its TUN device.
const TunName = "tailweave"

var errUnsupported = errors.New("tailweave does not support " + runtime.GOOS + " yet")

func (h *Host) Setup() error                      { return errUnsupported }
func (h *Host) setDomains(domains []string) error { return errUnsupported }
func (h *Host) Teardown()                         {}

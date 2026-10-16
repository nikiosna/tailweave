//go:build !linux && !darwin

package daemon

import (
	"errors"
	"net"
)

// peerUID is not implemented here, so only reading is allowed.
func peerUID(net.Conn) (int, error) { return -1, errors.New("peer credentials not supported") }

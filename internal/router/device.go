package router

import (
	"os"
	"sync"

	"github.com/tailscale/wireguard-go/tun"
)

// MTU is the MTU of the host TUN and of every tailnet's device.
const MTU = 1280

// queueLen is how many packets wait for a tailnet's engine before new ones are dropped.
const queueLen = 512

// device is a tailnet's end of the router: the tun.Device its Tailscale engine reads
// packets from and writes packets to. Reads return what the router queued for the
// tailnet; writes go straight to the router.
type device struct {
	in     chan []byte      // packets for the engine
	out    func(pkt []byte) // receives packets from the engine; must not keep pkt
	events chan tun.Event
	closed chan struct{}
	once   sync.Once
}

func newDevice(out func([]byte)) *device {
	d := &device{
		in:     make(chan []byte, queueLen),
		out:    out,
		events: make(chan tun.Event, 1),
		closed: make(chan struct{}),
	}
	d.events <- tun.EventUp
	return d
}

// queue hands a packet to the engine, or drops it if the engine falls behind.
func (d *device) queue(pkt []byte) {
	select {
	case d.in <- pkt:
	default:
	}
}

func (d *device) File() *os.File { return nil }

func (d *device) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	select {
	case <-d.closed:
		return 0, os.ErrClosed
	case pkt := <-d.in:
		sizes[0] = copy(bufs[0][offset:], pkt)
		n := 1
		// Hand over whatever else is already waiting, up to the batch size.
		for n < len(bufs) {
			select {
			case pkt := <-d.in:
				sizes[n] = copy(bufs[n][offset:], pkt)
				n++
			default:
				return n, nil
			}
		}
		return n, nil
	}
}

func (d *device) Write(bufs [][]byte, offset int) (int, error) {
	select {
	case <-d.closed:
		return 0, os.ErrClosed
	default:
	}
	for _, b := range bufs {
		d.out(b[offset:])
	}
	return len(bufs), nil
}

func (d *device) MTU() (int, error)        { return MTU, nil }
func (d *device) Name() (string, error)    { return "tailweave", nil }
func (d *device) Events() <-chan tun.Event { return d.events }
func (d *device) BatchSize() int           { return 32 }

func (d *device) Close() error {
	d.once.Do(func() {
		close(d.closed)
		close(d.events)
	})
	return nil
}

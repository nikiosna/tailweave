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

func (d *device) Read(slab []byte, packets []tun.ReadPacket) (int, error) {
	// Packets go into slab one after another, each preceded and the last followed by
	// tun.ReadPacketSpacing bytes. A packet is only taken from the queue while one of
	// MTU size still fits.
	off := tun.ReadPacketSpacing
	fits := func() bool { return off+MTU+tun.ReadPacketSpacing <= len(slab) }
	if len(packets) == 0 || !fits() {
		return 0, tun.ErrTooManySegments
	}
	select {
	case <-d.closed:
		return 0, os.ErrClosed
	case pkt := <-d.in:
		n := 0
		for {
			packets[n] = tun.ReadPacket{Offset: off, Size: copy(slab[off:off+MTU], pkt)}
			off += packets[n].Size + tun.ReadPacketSpacing
			n++
			// Hand over whatever else is already waiting, up to the batch size.
			if n == len(packets) || !fits() {
				return n, nil
			}
			select {
			case pkt = <-d.in:
			default:
				return n, nil
			}
		}
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

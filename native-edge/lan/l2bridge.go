package lan

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Dankular/GameBond.Core/bond"
)

// Switch ports of the client side bridge: the bond toward the exit and the adapter that carries
// the bridged device.
const (
	L2PortTunnel bond.PortID = 1
	L2PortLAN    bond.PortID = 2
)

// L2BridgeConfig is the policy of one bridged adapter. Zero rates mean unlimited.
type L2BridgeConfig struct {
	Name                                          string
	FrameMTU                                      int // largest frame carried (default bond.DefaultL2FrameMTU)
	MACLimit                                      int // learned MACs allowed behind the adapter (0 = unlimited)
	BroadcastPPS, MulticastPPS, UnknownUnicastPPS float64
}

// L2Bridge passes Ethernet frames between one adapter and the bond unchanged: the device keeps
// its own MAC, DHCP and addressing, and shares the exit's broadcast domain. It reuses the
// transport's switch core, so the same MAC cap, loop guard and storm control protect the
// domain from the client side, and frames are queued with the same classification the exit uses.
type L2Bridge struct {
	cfg        L2BridgeConfig
	io         FrameIO
	sw         *bond.L2Switch
	classifier *bond.Classifier
	submit     func(bond.QueuedPacket) bool
	now        func() time.Time

	mu                               sync.Mutex
	toTunnel, fromTunnel, oversize   atomic.Uint64
	queueFull, switchDrops, writeErr atomic.Uint64
}

// NewL2Bridge builds the bridge. submit queues a payload for the bond; classifier gives frames
// the same priority and duplication policy as IP packets.
func NewL2Bridge(cfg L2BridgeConfig, io FrameIO, classifier *bond.Classifier, submit func(bond.QueuedPacket) bool) *L2Bridge {
	if cfg.FrameMTU <= 0 {
		cfg.FrameMTU = bond.DefaultL2FrameMTU
	}
	return &L2Bridge{cfg: cfg, io: io, classifier: classifier, submit: submit, now: time.Now,
		sw: bond.NewL2Switch(bond.L2SwitchConfig{Ports: []bond.L2PortConfig{
			{ID: L2PortTunnel, Name: "bond"},
			{ID: L2PortLAN, Name: cfg.Name, MACLimit: cfg.MACLimit, BroadcastPPS: cfg.BroadcastPPS, MulticastPPS: cfg.MulticastPPS, UnknownUnicastPPS: cfg.UnknownUnicastPPS},
		}})}
}

// Run reads the adapter until ctx ends or the adapter fails.
func (b *L2Bridge) Run(ctx context.Context) error {
	sweep := b.now()
	for ctx.Err() == nil {
		f, err := b.io.Read(250 * time.Millisecond)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if now := b.now(); now.Sub(sweep) > 30*time.Second {
			b.sw.Sweep(now)
			sweep = now
		}
		if f != nil {
			b.FromAdapter(f)
		}
	}
	return nil
}

// FromAdapter handles one frame the device sent.
func (b *L2Bridge) FromAdapter(frame []byte) {
	now := b.now()
	if len(frame) > b.cfg.FrameMTU {
		b.oversize.Add(1)
		return
	}
	v := b.sw.Forward(L2PortLAN, frame, now)
	if v.Drop != bond.L2Forward {
		b.switchDrops.Add(1)
		return
	}
	for _, out := range v.Out {
		if out != L2PortTunnel {
			continue
		}
		c, _, err := b.classifier.ClassifyFrame(frame, now)
		if err != nil {
			b.switchDrops.Add(1)
			return
		}
		q := bond.QueuedPacket{Payload: bond.WrapFrame(frame), Flow: c.Info.Flow, Priority: c.Policy.Priority,
			Replicate: c.Policy.Replicate, Tag: c.Tag, ClassCapacity: c.Policy.Capacity, Expires: now.Add(5 * time.Second)}
		if c.Policy.MaxAgeMS > 0 {
			q.Expires = now.Add(time.Duration(c.Policy.MaxAgeMS * float64(time.Millisecond)))
		}
		if b.submit(q) {
			b.toTunnel.Add(1)
		} else {
			b.queueFull.Add(1)
		}
	}
}

// Deliver handles an L2 payload that arrived over the bond and reports whether it was an L2
// payload (so the caller never hands it to the IP stack, delivered or not).
func (b *L2Bridge) Deliver(payload []byte) bool {
	frame, ok := bond.UnwrapFrame(payload)
	if !ok {
		return false
	}
	v := b.sw.Forward(L2PortTunnel, frame, b.now())
	if v.Drop != bond.L2Forward {
		b.switchDrops.Add(1)
		return true
	}
	for _, out := range v.Out {
		if out != L2PortLAN {
			continue
		}
		if err := b.io.Write(frame); err != nil {
			b.writeErr.Add(1)
			continue
		}
		b.fromTunnel.Add(1)
	}
	return true
}

// Stats is the panel view.
func (b *L2Bridge) Stats() map[string]any {
	st := b.sw.Stats()
	return map[string]any{
		"name": b.cfg.Name, "to_tunnel": b.toTunnel.Load(), "from_tunnel": b.fromTunnel.Load(),
		"oversize": b.oversize.Load(), "queue_full": b.queueFull.Load(), "switch_drops": b.switchDrops.Load(), "write_errors": b.writeErr.Load(),
		"fdb_size": st.FDBSize, "learned": st.Learned, "moves": st.Moves, "flooded": st.Flooded, "forwarded": st.Forwarded,
		"drop_storm": st.DropStorm, "drop_mac_limit": st.DropMACLimit, "drop_blocked": st.DropBlocked, "loop_holds": st.LoopHolds,
	}
}

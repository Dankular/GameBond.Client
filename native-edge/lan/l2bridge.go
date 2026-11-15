package lan

import (
	"context"
	"fmt"
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
	TunnelVLAN, LANVLAN                           bond.L2VLANPolicy
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
	trace                            l2Trace
	read                             atomic.Uint64
}

// NewL2Bridge builds the bridge. submit queues a payload for the bond; classifier gives frames
// the same priority and duplication policy as IP packets.
func NewL2Bridge(cfg L2BridgeConfig, io FrameIO, classifier *bond.Classifier, submit func(bond.QueuedPacket) bool) *L2Bridge {
	if cfg.FrameMTU <= 0 {
		cfg.FrameMTU = bond.DefaultL2FrameMTU
	}
	cfg.FrameMTU += max(cfg.TunnelVLAN.EgressTagOverhead(), cfg.LANVLAN.EgressTagOverhead())
	return &L2Bridge{cfg: cfg, io: io, classifier: classifier, submit: submit, now: time.Now,
		sw: bond.NewL2Switch(bond.L2SwitchConfig{Ports: []bond.L2PortConfig{
			{ID: L2PortTunnel, Name: "bond", VLAN: cfg.TunnelVLAN},
			{ID: L2PortLAN, Name: cfg.Name, MACLimit: cfg.MACLimit, BroadcastPPS: cfg.BroadcastPPS, MulticastPPS: cfg.MulticastPPS, UnknownUnicastPPS: cfg.UnknownUnicastPPS, VLAN: cfg.LANVLAN},
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
			b.read.Add(1)
			b.FromAdapter(f)
		}
	}
	return nil
}

// FromAdapter handles one frame the device sent.
func (b *L2Bridge) FromAdapter(frame []byte) {
	now := b.now()
	frameMTU := b.tunnelFrameMTU(frame)
	if len(frame) > frameMTU {
		b.oversize.Add(1)
		fwd, back, ok := oversizeIPv4(frame, frameMTU)
		if !ok {
			b.trace.note("drop-oversize", frame)
			return
		}
		if len(back) > 0 {
			b.trace.note("icmp-frag-needed", frame)
		} else {
			b.trace.note("fragmented", frame)
		}
		for _, f := range back {
			b.io.Write(padWire(f))
		}
		for _, f := range fwd {
			b.FromAdapter(f)
		}
		return
	}
	v := b.sw.Forward(L2PortLAN, frame, now)
	if v.Drop != bond.L2Forward {
		b.switchDrops.Add(1)
		b.trace.note(fmt.Sprintf("drop-switch-%v", v.Drop), frame)
		return
	}
	for _, out := range v.Out {
		if out != L2PortTunnel {
			continue
		}
		outFrame := v.FrameFor(out, frame)
		if len(outFrame) > b.cfg.FrameMTU {
			b.oversize.Add(1)
			b.trace.note("drop-oversize-up", outFrame)
			continue
		}
		c, _, err := b.classifier.ClassifyFrame(outFrame, now)
		if err != nil {
			b.switchDrops.Add(1)
			b.trace.note("drop-classify", frame)
			return
		}
		q := bond.QueuedPacket{Payload: bond.WrapFrame(outFrame), Flow: c.Info.Flow, Priority: c.Policy.Priority,
			Replicate: c.Policy.Replicate, Tag: c.Tag, ClassCapacity: c.Policy.Capacity, Expires: now.Add(5 * time.Second)}
		if c.Policy.MaxAgeMS > 0 {
			q.Expires = now.Add(time.Duration(c.Policy.MaxAgeMS * float64(time.Millisecond)))
		}
		if b.submit(q) {
			b.toTunnel.Add(1)
			b.trace.note("up", frame)
		} else {
			b.queueFull.Add(1)
			b.trace.note("drop-queue-full", frame)
		}
	}
}

func (b *L2Bridge) tunnelFrameMTU(frame []byte) int {
	tagGrowth := b.cfg.TunnelVLAN.EgressTagOverhead()
	if info, err := bond.ParseFrame(frame); err == nil {
		tagGrowth -= int(info.VLANDepth) * 4
	}
	if tagGrowth < 0 {
		tagGrowth = 0
	}
	return b.cfg.FrameMTU - tagGrowth
}

// minWireFrame is the size frames are padded to before they reach the adapter.
const minWireFrame = 120

// padWire pads a runt frame the way a NIC would before it reaches the raw adapter write.
func padWire(f []byte) []byte {
	if len(f) >= minWireFrame {
		return f
	}
	return append(append(make([]byte, 0, minWireFrame), f...), make([]byte, minWireFrame-len(f))...)
}

// Deliver handles an L2 payload that arrived over the bond and reports whether it was an L2
// payload (so the caller never hands it to the IP stack, delivered or not).
func (b *L2Bridge) Deliver(payload []byte) bool {
	frame, ok := bond.UnwrapFrame(payload)
	if !ok {
		return false
	}
	if len(frame) > b.cfg.FrameMTU {
		b.oversize.Add(1)
		b.trace.note("drop-oversize-down", frame)
		return true
	}
	v := b.sw.Forward(L2PortTunnel, frame, b.now())
	if v.Drop != bond.L2Forward {
		b.switchDrops.Add(1)
		b.trace.note(fmt.Sprintf("down-drop-switch-%v", v.Drop), frame)
		return true
	}
	for _, out := range v.Out {
		if out != L2PortLAN {
			continue
		}
		outFrame := v.FrameFor(out, frame)
		if len(outFrame) > b.cfg.FrameMTU {
			b.oversize.Add(1)
			b.trace.note("drop-oversize-down", outFrame)
			continue
		}
		// A NIC pads runt frames in hardware; the raw driver write does not, and devices never saw
		// the 42-byte ARP replies. Pad here, at the adapter, so the exit stays a pure stack.
		wire := outFrame
		if len(wire) < minWireFrame {
			wire = append(append(make([]byte, 0, minWireFrame), wire...), make([]byte, minWireFrame-len(wire))...)
		}
		if err := b.io.Write(wire); err != nil {
			b.writeErr.Add(1)
			b.trace.note("down-write-error", frame)
			continue
		}
		b.fromTunnel.Add(1)
		b.trace.note("down", frame)
	}
	return true
}

// FramesRead is how many frames the adapter has delivered to the bridge since it opened.
func (b *L2Bridge) FramesRead() uint64 { return b.read.Load() }

// BridgedDevice is one MAC address learned behind the bridged adapter.
type BridgedDevice struct {
	MAC     string `json:"mac"`
	VLAN    uint16 `json:"vlan,omitempty"`
	AgeSecs int    `json:"age_secs"`
}

// Devices lists the MACs currently learned on the LAN-facing port, excluding the bond/tunnel
// side's own FDB entries. Used to show individual connected devices in the topology view.
func (b *L2Bridge) Devices() []BridgedDevice {
	entries := b.sw.FDBEntries()
	out := make([]BridgedDevice, 0, len(entries))
	for _, e := range entries {
		if e.Port != L2PortLAN {
			continue
		}
		out = append(out, BridgedDevice{
			MAC:     fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", e.MAC[0], e.MAC[1], e.MAC[2], e.MAC[3], e.MAC[4], e.MAC[5]),
			VLAN:    e.VLAN,
			AgeSecs: int(e.Age.Seconds()),
		})
	}
	return out
}

// Stats is the panel view.
func (b *L2Bridge) Stats() map[string]any {
	st := b.sw.Stats()
	m := map[string]any{
		"name": b.cfg.Name, "to_tunnel": b.toTunnel.Load(), "from_tunnel": b.fromTunnel.Load(),
		"oversize": b.oversize.Load(), "queue_full": b.queueFull.Load(), "switch_drops": b.switchDrops.Load(), "write_errors": b.writeErr.Load(),
		"fdb_size": st.FDBSize, "learned": st.Learned, "moves": st.Moves, "aged": st.Aged, "flooded": st.Flooded, "forwarded": st.Forwarded,
		"drop_storm": st.DropStorm, "drop_mac_limit": st.DropMACLimit, "drop_blocked": st.DropBlocked, "drop_isolated": st.DropIsolated,
		"drop_other": st.DropOther, "drop_bpdu_guard": st.DropBPDUGuard, "drop_vlan": st.DropVLAN, "drop_ra_guard": st.DropRAGuard,
		"drop_source_guard": st.DropSourceGuard, "drop_copp": st.DropCoPP, "drop_unauthorized": st.DropUnauthorized, "drop_rstp": st.DropRSTP,
		"bpdu_guards": st.BPDUGuards, "loop_holds": st.LoopHolds, "multicast_groups": st.MulticastGroups, "multicast_members": st.MulticastMembers,
	}
	for k, v := range b.trace.snapshot() {
		m[k] = v
	}
	return m
}

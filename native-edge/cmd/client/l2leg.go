package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	"gamebond.local/nativeedge/gvisorlink"
	"gamebond.local/nativeedge/lan"
	"github.com/Dankular/GameBond.Core/bond"
)

// l2BridgeConfig is the optional "l2_bridge" section: one adapter whose device is bridged onto
// the exit's network as is (own MAC, own DHCP) instead of terminated by the LAN gateway. The
// adapter must not also appear in lan_devices or uplinks.
type l2BridgeConfig struct {
	Enabled           bool              `json:"enabled"`
	Name              string            `json:"name"`
	InterfaceID       string            `json:"interface_id"`
	InterfaceName     string            `json:"interface_name"`
	FrameMTU          int               `json:"frame_mtu"`
	MACLimit          int               `json:"mac_limit"`
	BroadcastPPS      float64           `json:"broadcast_pps"`
	MulticastPPS      float64           `json:"multicast_pps"`
	UnknownUnicastPPS float64           `json:"unknown_unicast_pps"`
	TunnelVLAN        bond.L2VLANPolicy `json:"tunnel_vlan,omitempty"`
	LANVLAN           bond.L2VLANPolicy `json:"lan_vlan,omitempty"`
	// DHCP makes the client's gVisor link answer DHCP for the bridged device (fixed reservations
	// only) instead of the exit. Omit it to leave DHCP to the exit.
	DHCP *l2DHCPConfig `json:"dhcp,omitempty"`
}

// l2DHCPConfig is the "l2_bridge.dhcp" section.
type l2DHCPConfig struct {
	Reservations []l2Reservation `json:"reservations"`
	DNS          []string        `json:"dns,omitempty"`
	LeaseSeconds int             `json:"lease_seconds,omitempty"`
}

type l2Reservation struct {
	MAC  string `json:"mac"`
	IP   string `json:"ip"`
	Name string `json:"name,omitempty"`
}

// gvisorDHCP converts the section into the link's DHCP settings (nil when absent).
func (c *l2BridgeConfig) gvisorDHCP() (*gvisorlink.DHCPConfig, error) {
	if c == nil {
		return nil, nil
	}
	if c.DHCP == nil {
		// No local override: still serve whatever the exit delegates in its provisioning.
		return &gvisorlink.DHCPConfig{}, nil
	}
	out := &gvisorlink.DHCPConfig{LeaseSeconds: c.DHCP.LeaseSeconds}
	for i, r := range c.DHCP.Reservations {
		mac, err := net.ParseMAC(r.MAC)
		if err != nil || len(mac) != 6 {
			return nil, fmt.Errorf("l2_bridge.dhcp.reservations[%d].mac is not an Ethernet address", i)
		}
		ip, err := netip.ParseAddr(r.IP)
		if err != nil || !ip.Is4() {
			return nil, fmt.Errorf("l2_bridge.dhcp.reservations[%d].ip is not an IPv4 address", i)
		}
		var m [6]byte
		copy(m[:], mac)
		out.Reservations = append(out.Reservations, lan.Reservation{MAC: m, IP: ip, Name: r.Name})
	}
	for i, d := range c.DHCP.DNS {
		a, err := netip.ParseAddr(d)
		if err != nil || !a.Is4() {
			return nil, fmt.Errorf("l2_bridge.dhcp.dns[%d] is not an IPv4 address", i)
		}
		out.DNS = append(out.DNS, a)
	}
	if len(out.Reservations) == 0 {
		return nil, errors.New("l2_bridge.dhcp needs at least one reservation (omit the section to use the exit's delegation)")
	}
	if err := out.Validate(); err != nil {
		return nil, fmt.Errorf("l2_bridge.dhcp: %w", err)
	}
	return out, nil
}

func tunnelClientMTU(outerMTU int, l2cfg *l2BridgeConfig) int {
	mtu := outerMTU + bond.Overhead + 24
	if l2cfg != nil {
		mtu += l2cfg.EgressTagOverhead()
	}
	return mtu
}

func (c *l2BridgeConfig) EgressTagOverhead() int {
	if c == nil {
		return 0
	}
	return max(c.TunnelVLAN.EgressTagOverhead(), c.LANVLAN.EgressTagOverhead())
}

func parseL2Bridge(raw json.RawMessage) (*l2BridgeConfig, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	var c l2BridgeConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("l2_bridge: %w", err)
	}
	if !c.Enabled {
		return nil, nil
	}
	if c.InterfaceID == "" && c.InterfaceName == "" {
		return nil, errors.New("l2_bridge needs interface_id (Windows) or interface_name (Linux)")
	}
	if c.Name == "" {
		c.Name = "l2-bridge"
	}
	if c.FrameMTU == 0 {
		c.FrameMTU = bond.DefaultL2FrameMTU
	}
	if c.FrameMTU < 600 || c.FrameMTU > 1500 {
		return nil, errors.New("l2_bridge frame_mtu must be 600-1500")
	}
	if c.MACLimit < 0 || c.MACLimit > 4096 || c.BroadcastPPS < 0 || c.MulticastPPS < 0 || c.UnknownUnicastPPS < 0 {
		return nil, errors.New("l2_bridge limits must not be negative (mac_limit <= 4096)")
	}
	if err := c.TunnelVLAN.Validate(); err != nil {
		return nil, fmt.Errorf("l2_bridge.tunnel_vlan: %w", err)
	}
	if err := c.LANVLAN.Validate(); err != nil {
		return nil, fmt.Errorf("l2_bridge.lan_vlan: %w", err)
	}
	if _, err := c.gvisorDHCP(); err != nil {
		return nil, err
	}
	return &c, nil
}

// l2LinkBounced is signalled when the bridged adapter's link went down and came back.
var l2LinkBounced = make(chan struct{}, 1)

var (
	activeL2         atomic.Pointer[lan.L2Bridge]
	l2Configured     atomic.Bool
	l2Promiscuous    atomic.Bool   // the adapter's hardware receive filter is held open
	l2PromiscuousWhy atomic.Value  // string: why it is not
	l2Orphans        atomic.Uint64 // L2 payloads that arrived while no bridge was up
	l2LastError      atomic.Value  // string
)

// deliverL2 hands an L2 payload to the running bridge. It reports whether the payload was L2, so
// the caller never passes a frame to the IP stack even when the adapter is down.
func deliverL2(p []byte) bool {
	if len(p) == 0 || p[0] != bond.L2Marker {
		return false
	}
	if b := activeL2.Load(); b != nil {
		b.Deliver(p)
	} else {
		l2Orphans.Add(1)
	}
	return true
}

// l2Status is the panel's l2_bridge block.
func l2Status() any {
	if !l2Configured.Load() {
		return nil
	}
	st := map[string]any{"up": false, "orphans": l2Orphans.Load()}
	if b := activeL2.Load(); b != nil {
		st = b.Stats()
		st["up"], st["orphans"] = true, l2Orphans.Load()
		st["devices"] = b.Devices()
	}
	st["promiscuous"] = l2Promiscuous.Load()
	if why, _ := l2PromiscuousWhy.Load().(string); why != "" {
		st["promiscuous_error"] = why
	}
	if e, _ := l2LastError.Load().(string); e != "" {
		st["error"] = e
	}
	return st
}

// startL2Bridge keeps the bridged adapter open: an unplugged or failing adapter is retried every
// five seconds and never stops the tunnel.
func startL2Bridge(ctx context.Context, c *l2BridgeConfig, classifier *bond.Classifier, submit func(bond.QueuedPacket) bool, local lan.LocalHandler, out func(map[string]any)) {
	l2Configured.Store(true)
	go holdPromiscuous(ctx, c.InterfaceID, out)
	go func() {
		for ctx.Err() == nil {
			io, err := openFrameIO(lan.DeviceConfig{Name: c.Name, InterfaceID: c.InterfaceID, InterfaceName: c.InterfaceName})
			if err == nil {
				b := lan.NewL2Bridge(lan.L2BridgeConfig{Name: c.Name, FrameMTU: c.FrameMTU, MACLimit: c.MACLimit,
					TunnelVLAN: c.TunnelVLAN, LANVLAN: c.LANVLAN,
					BroadcastPPS: c.BroadcastPPS, MulticastPPS: c.MulticastPPS, UnknownUnicastPPS: c.UnknownUnicastPPS}, io, classifier, submit)
				b.SetLocalHandler(local)
				activeL2.Store(b)
				l2LastError.Store("")
				out(map[string]any{"status": "l2_bridge_up", "name": c.Name})
				// An adapter restart invalidates the driver's tap on it: the handle stays open but never
				// delivers another frame. The promiscuous watcher signals each link-down then link-up, and
				// the bridge reopens the adapter.
				runCtx, stopRun := context.WithCancel(ctx)
				// A tap that has delivered nothing for 15 s while the link is up is deaf (the driver can lose
				// its binding on a client restart); reopen it, at most every 15 s. A quiet segment still
				// carries ARP and DHCP chatter, so silence is a fault, not idleness.
				go func() {
					tick := time.NewTicker(5 * time.Second)
					defer tick.Stop()
					lastRead, since := uint64(0), time.Now()
					for {
						select {
						case <-runCtx.Done():
							return
						case <-tick.C:
							if n := b.FramesRead(); n != lastRead {
								lastRead, since = n, time.Now()
							} else if time.Since(since) >= 15*time.Second && l2Promiscuous.Load() {
								out(map[string]any{"status": "l2_bridge_reopen", "name": c.Name, "reason": "no frames read for 15 s"})
								stopRun()
								return
							}
						}
					}
				}()
				go func() {
					select {
					case <-l2LinkBounced:
						out(map[string]any{"status": "l2_bridge_reopen", "name": c.Name, "reason": "adapter link bounced"})
						stopRun()
					case <-runCtx.Done():
					}
				}()
				err = b.Run(runCtx)
				stopRun()
				activeL2.Store(nil)
				io.Close()
			}
			if err != nil {
				if prev, _ := l2LastError.Load().(string); prev != err.Error() {
					out(map[string]any{"status": "l2_bridge_down", "name": c.Name, "error": err.Error()})
				}
				l2LastError.Store(err.Error())
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}()
}

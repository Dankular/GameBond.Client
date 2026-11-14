package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"gamebond.local/nativeedge/lan"
	"github.com/Dankular/GameBond.Core/bond"
)

// l2BridgeConfig is the optional "l2_bridge" section: one adapter whose device is bridged onto
// the exit's network as is (own MAC, own DHCP) instead of terminated by the LAN gateway. The
// adapter must not also appear in lan_devices or uplinks.
type l2BridgeConfig struct {
	Enabled           bool    `json:"enabled"`
	Name              string  `json:"name"`
	InterfaceID       string  `json:"interface_id"`
	InterfaceName     string  `json:"interface_name"`
	FrameMTU          int     `json:"frame_mtu"`
	MACLimit          int     `json:"mac_limit"`
	BroadcastPPS      float64 `json:"broadcast_pps"`
	MulticastPPS      float64 `json:"multicast_pps"`
	UnknownUnicastPPS float64 `json:"unknown_unicast_pps"`
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
	return &c, nil
}

var (
	activeL2     atomic.Pointer[lan.L2Bridge]
	l2Configured atomic.Bool
	l2Orphans    atomic.Uint64 // L2 payloads that arrived while no bridge was up
	l2LastError  atomic.Value  // string
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
	}
	if e, _ := l2LastError.Load().(string); e != "" {
		st["error"] = e
	}
	return st
}

// startL2Bridge keeps the bridged adapter open: an unplugged or failing adapter is retried every
// five seconds and never stops the tunnel.
func startL2Bridge(ctx context.Context, c *l2BridgeConfig, classifier *bond.Classifier, submit func(bond.QueuedPacket) bool, out func(map[string]any)) {
	l2Configured.Store(true)
	go holdPromiscuous(ctx, c.InterfaceID, out)
	go func() {
		for ctx.Err() == nil {
			io, err := openFrameIO(lan.DeviceConfig{Name: c.Name, InterfaceID: c.InterfaceID, InterfaceName: c.InterfaceName})
			if err == nil {
				b := lan.NewL2Bridge(lan.L2BridgeConfig{Name: c.Name, FrameMTU: c.FrameMTU, MACLimit: c.MACLimit,
					BroadcastPPS: c.BroadcastPPS, MulticastPPS: c.MulticastPPS, UnknownUnicastPPS: c.UnknownUnicastPPS}, io, classifier, submit)
				activeL2.Store(b)
				l2LastError.Store("")
				out(map[string]any{"status": "l2_bridge_up", "name": c.Name})
				err = b.Run(ctx)
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

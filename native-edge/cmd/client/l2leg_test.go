package main

import (
	"encoding/json"
	"testing"

	"github.com/Dankular/GameBond.Core/bond"
)

func TestL2BridgeConfigIsOptInAndValidated(t *testing.T) {
	for _, raw := range []string{``, `null`, `{"enabled":false}`} {
		if c, err := parseL2Bridge(json.RawMessage(raw)); c != nil || err != nil {
			t.Fatalf("%q must be off: %v %v", raw, c, err)
		}
	}
	c, err := parseL2Bridge(json.RawMessage(`{"enabled":true,"interface_id":"{abc}","mac_limit":4}`))
	if err != nil || c.Name != "l2-bridge" || c.FrameMTU != bond.DefaultL2FrameMTU || c.MACLimit != 4 {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	for _, bad := range []string{`{"enabled":true}`, `{"enabled":true,"interface_id":"x","frame_mtu":50}`, `{"enabled":true,"interface_id":"x","mac_limit":-1}`, `{"enabled":true,"interface_id":"x","vlan":5}`} {
		if _, err := parseL2Bridge(json.RawMessage(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestL2PayloadsNeverReachTheIPStack(t *testing.T) {
	if deliverL2([]byte{0x45, 0, 0, 20}) {
		t.Fatal("IPv4 taken as L2")
	}
	before := l2Orphans.Load()
	if !deliverL2(bond.WrapFrame(make([]byte, 60))) || l2Orphans.Load() != before+1 {
		t.Fatal("an L2 payload with no bridge up must be consumed and counted")
	}
}

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

func TestL2BridgeConfigEgressTagOverhead(t *testing.T) {
	qinq, err := parseL2Bridge(json.RawMessage(`{"enabled":true,"interface_name":"eth0","tunnel_vlan":{"service_vlan":100,"allowed_customer_vlans":[10,20]}}`))
	if err != nil || qinq.EgressTagOverhead() != 8 {
		t.Fatalf("Q-in-Q trunk overhead=%v err=%v", qinq, err)
	}
	customerAccess, err := parseL2Bridge(json.RawMessage(`{"enabled":true,"interface_name":"eth0","lan_vlan":{"service_vlan":100,"customer_vlan":10}}`))
	if err != nil || customerAccess.EgressTagOverhead() != 0 {
		t.Fatalf("customer access should strip tags: %v err=%v", customerAccess, err)
	}
	singleTag, err := parseL2Bridge(json.RawMessage(`{"enabled":true,"interface_name":"eth0","lan_vlan":{"allowed_vlans":[10]}}`))
	if err != nil || singleTag.EgressTagOverhead() != 4 {
		t.Fatalf("single-tag trunk overhead=%v err=%v", singleTag, err)
	}
}

func TestTunnelClientMTUIncludesL2EgressTagRoom(t *testing.T) {
	outerMTU := 1400
	base := outerMTU + bond.Overhead + 24
	for _, test := range []struct {
		name string
		cfg  *l2BridgeConfig
		want int
	}{
		{name: "bridge disabled", want: base},
		{name: "single tag", cfg: &l2BridgeConfig{LANVLAN: bond.L2VLANPolicy{AllowedVLANs: []uint16{10}}}, want: base + 4},
		{name: "qinq", cfg: &l2BridgeConfig{TunnelVLAN: bond.L2VLANPolicy{ServiceVLAN: 100, AllowedCustomerVLANs: []uint16{10}}}, want: base + 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := tunnelClientMTU(outerMTU, test.cfg); got != test.want {
				t.Fatalf("client MTU=%d, want %d", got, test.want)
			}
		})
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

func TestL2BridgeDHCPSection(t *testing.T) {
	good := `{"enabled":true,"interface_name":"eth1","dhcp":{"reservations":[{"mac":"02:11:22:33:44:55","ip":"10.77.0.50","name":"ps5"}],"dns":["1.1.1.1"],"lease_seconds":600}}`
	c, err := parseL2Bridge(json.RawMessage(good))
	if err != nil || c == nil {
		t.Fatalf("valid dhcp section rejected: %v", err)
	}
	if d, err := c.gvisorDHCP(); err != nil || len(d.Reservations) != 1 || d.LeaseSeconds != 600 {
		t.Fatalf("bad conversion: %v %v", d, err)
	}
	for _, bad := range []string{
		`{"enabled":true,"interface_name":"e","dhcp":{"reservations":[]}}`,
		`{"enabled":true,"interface_name":"e","dhcp":{"reservations":[{"mac":"zz","ip":"10.0.0.5"}]}}`,
		`{"enabled":true,"interface_name":"e","dhcp":{"reservations":[{"mac":"02:11:22:33:44:55","ip":"::1"}]}}`,
		`{"enabled":true,"interface_name":"e","dhcp":{"reservations":[{"mac":"01:11:22:33:44:55","ip":"10.0.0.5"}]}}`,
	} {
		if _, err := parseL2Bridge(json.RawMessage(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

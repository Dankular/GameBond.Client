package gvisorlink

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"gamebond.local/nativeedge/lan"
	"github.com/Dankular/GameBond.Core/bond"
)

var ps5MAC = [6]byte{0x02, 0x11, 0x22, 0x33, 0x44, 0x55}

func ipChecksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

// dhcpRequest builds a broadcast DHCP client frame (kind 1 DISCOVER, 3 REQUEST).
func dhcpRequest(mac [6]byte, kind byte, requested netip.Addr, server netip.Addr) []byte {
	body := make([]byte, 240)
	body[0], body[1], body[2] = 1, 1, 6
	copy(body[4:8], []byte{9, 8, 7, 6})
	copy(body[28:34], mac[:])
	copy(body[236:240], []byte{0x63, 0x82, 0x53, 0x63})
	body = append(body, 53, 1, kind)
	if requested.IsValid() {
		a := requested.As4()
		body = append(body, 50, 4)
		body = append(body, a[:]...)
		s := server.As4()
		body = append(body, 54, 4)
		body = append(body, s[:]...)
	}
	body = append(body, 255)
	udp := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint16(udp[0:], 68)
	binary.BigEndian.PutUint16(udp[2:], 67)
	binary.BigEndian.PutUint16(udp[4:], uint16(8+len(body)))
	udp = append(udp, body...)
	ip := make([]byte, 20, 20+len(udp))
	ip[0], ip[8], ip[9] = 0x45, 64, 17
	binary.BigEndian.PutUint16(ip[2:], uint16(20+len(udp)))
	copy(ip[16:20], []byte{255, 255, 255, 255})
	binary.BigEndian.PutUint16(ip[10:], ipChecksum(ip))
	ip = append(ip, udp...)
	frame := append([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, mac[:]...)
	frame = append(frame, 0x08, 0x00)
	return append(frame, ip...)
}

func dhcpOptions(reply []byte) (yiaddr netip.Addr, opts map[byte][]byte) {
	udp := reply[14+20:]
	b := udp[8:]
	yiaddr = netip.AddrFrom4([4]byte(b[16:20]))
	opts = map[byte][]byte{}
	for i := 240; i < len(b) && b[i] != 255; i += 2 + int(b[i+1]) {
		opts[b[i]] = b[i+2 : i+2+int(b[i+1])]
	}
	return
}

type testLink struct {
	*Link
	out chan []byte
}

func newDHCPLink(t *testing.T) *testLink {
	t.Helper()
	out := make(chan []byte, 16)
	l, err := New(Options{ClientIP: netip.MustParseAddr("10.200.0.2"), Send: func(bond.QueuedPacket) bool { return true }, DeliverIP: func([]byte) {},
		LANWrite: func(f []byte) { out <- f },
		DHCP: &DHCPConfig{Reservations: []lan.Reservation{{MAC: ps5MAC, IP: netip.MustParseAddr("10.77.0.50"), Name: "ps5"}},
			DNS: []netip.Addr{netip.MustParseAddr("1.1.1.1")}, LeaseSeconds: 600}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	return &testLink{l, out}
}

// exchange sends one client frame and returns the reply the stack emitted (nil when none arrives).
func (l *testLink) exchange(t *testing.T, frame []byte) (reply []byte, consumed bool) {
	t.Helper()
	if _, consumed = l.HandleLANFrame(frame); !consumed {
		return nil, false
	}
	select {
	case reply = <-l.out:
	case <-time.After(300 * time.Millisecond):
	}
	return reply, true
}

func provision(t *testing.T, l *Link) {
	t.Helper()
	p := &bond.GatewayProvisioning{Revision: 1, TopologyID: "t", SegmentID: "s", ExitID: "e", Mode: bond.GatewayPrimary, DHCPAuthorityID: "e",
		Link: bond.GatewayLinkProvisioning{ClientAddress: "10.77.0.2/24", ExitAddress: "10.77.0.1", MTU: 1400}}
	if err := l.Configure(p); err != nil {
		t.Fatal(err)
	}
}

func TestFixedReservationOfferAndAckThroughGVisor(t *testing.T) {
	l := newDHCPLink(t)
	if _, consumed := l.HandleLANFrame(dhcpRequest(ps5MAC, 1, netip.Addr{}, netip.Addr{})); consumed {
		t.Fatal("an inactive link must leave DHCP to the exit")
	}
	provision(t, l.Link)
	reply, consumed := l.exchange(t, dhcpRequest(ps5MAC, 1, netip.Addr{}, netip.Addr{}))
	if !consumed || reply == nil {
		t.Fatal("no OFFER from the gVisor stack")
	}
	if !bytes.Equal(reply[:6], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}) || !bytes.Equal(reply[6:12], lanMACBytes) {
		t.Fatalf("reply must be an Ethernet broadcast from the virtual LAN NIC: % x", reply[:12])
	}
	if !bytes.Equal(reply[14+12:14+16], []byte{10, 77, 0, 2}) || !bytes.Equal(reply[14+16:14+20], []byte{255, 255, 255, 255}) {
		t.Fatalf("reply must come from the client bond address to the limited broadcast: % x", reply[14:14+20])
	}
	ip, opts := dhcpOptions(reply)
	want := netip.MustParseAddr("10.77.0.50")
	if ip != want || opts[53][0] != 2 || !bytes.Equal(opts[54], []byte{10, 77, 0, 2}) || !bytes.Equal(opts[3], []byte{10, 77, 0, 1}) ||
		!bytes.Equal(opts[1], []byte{255, 255, 255, 0}) || !bytes.Equal(opts[6], []byte{10, 77, 0, 2}) {
		t.Fatalf("bad OFFER %v %v", ip, opts)
	}
	if !bytes.Equal(opts[26], []byte{0x05, 0x78}) || !bytes.Equal(opts[28], []byte{10, 77, 0, 255}) || !bytes.Equal(opts[51], []byte{0, 0, 2, 0x58}) ||
		!bytes.Equal(opts[58], []byte{0, 0, 1, 0x2c}) || !bytes.Equal(opts[59], []byte{0, 0, 2, 0x0d}) {
		t.Fatalf("OFFER must carry the link MTU 1400, the broadcast address and renewal timers: %v", opts)
	}
	reply, _ = l.exchange(t, dhcpRequest(ps5MAC, 3, want, netip.MustParseAddr("10.77.0.2")))
	if reply == nil {
		t.Fatal("no ACK")
	}
	if ip, opts = dhcpOptions(reply); ip != want || opts[53][0] != 5 {
		t.Fatalf("bad ACK %v %v", ip, opts)
	}
	reply, _ = l.exchange(t, dhcpRequest(ps5MAC, 3, netip.MustParseAddr("10.77.0.99"), netip.MustParseAddr("10.77.0.2")))
	if reply == nil {
		t.Fatal("no NAK")
	}
	if _, opts = dhcpOptions(reply); opts[53][0] != 6 {
		t.Fatal("a different address must be NAKed")
	}
}

func TestUnreservedDeviceGetsNoAddress(t *testing.T) {
	l := newDHCPLink(t)
	provision(t, l.Link)
	other := [6]byte{0x02, 0xaa, 0xbb, 0xcc, 0xdd, 0xee}
	if reply, consumed := l.exchange(t, dhcpRequest(other, 1, netip.Addr{}, netip.Addr{})); !consumed || reply != nil {
		t.Fatalf("an unreserved device must be consumed without an offer, got consumed=%v reply=%v", consumed, reply != nil)
	}
}

func TestSpoofedChaddrIsDropped(t *testing.T) {
	l := newDHCPLink(t)
	provision(t, l.Link)
	frame := dhcpRequest(ps5MAC, 1, netip.Addr{}, netip.Addr{})
	copy(frame[6:12], []byte{0x02, 0x99, 0x99, 0x99, 0x99, 0x99}) // Ethernet source differs from chaddr
	if reply, consumed := l.exchange(t, frame); !consumed || reply != nil {
		t.Fatal("a request whose chaddr differs from its Ethernet source must be dropped")
	}
}

func TestNonDHCPFramesAreNotConsumed(t *testing.T) {
	l := newDHCPLink(t)
	provision(t, l.Link)
	arp := make([]byte, 60)
	copy(arp[:6], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	copy(arp[6:12], ps5MAC[:])
	arp[12], arp[13] = 0x08, 0x06
	if _, consumed := l.HandleLANFrame(arp); consumed {
		t.Fatal("ARP must keep bridging to the exit; the virtual LAN NIC must never see it")
	}
}

func TestDisabledProvisioningStopsDHCP(t *testing.T) {
	l := newDHCPLink(t)
	provision(t, l.Link)
	if err := l.Configure(&bond.GatewayProvisioning{Mode: bond.GatewayDisabled}); err != nil {
		t.Fatal(err)
	}
	if _, consumed := l.HandleLANFrame(dhcpRequest(ps5MAC, 1, netip.Addr{}, netip.Addr{})); consumed {
		t.Fatal("DHCP must stop when the exit disables the gateway")
	}
}

func TestReservationOutsideProvisionedSubnetIsRejected(t *testing.T) {
	l, err := New(Options{ClientIP: netip.MustParseAddr("10.200.0.2"), Send: func(bond.QueuedPacket) bool { return true }, DeliverIP: func([]byte) {},
		LANWrite: func([]byte) {},
		DHCP:     &DHCPConfig{Reservations: []lan.Reservation{{MAC: ps5MAC, IP: netip.MustParseAddr("192.168.9.9")}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	p := &bond.GatewayProvisioning{Revision: 1, TopologyID: "t", SegmentID: "s", ExitID: "e", Mode: bond.GatewayPrimary, DHCPAuthorityID: "e",
		Link: bond.GatewayLinkProvisioning{ClientAddress: "10.77.0.2/24", ExitAddress: "10.77.0.1", MTU: 1400}}
	if err := l.Configure(p); err == nil || l.Active() {
		t.Fatal("a reservation outside the bond subnet must fail provisioning, not activate the link")
	}
}

func TestProvisionedHostsDriveDHCPAndAnEmptyProvisioningLeavesItToTheExit(t *testing.T) {
	out := make(chan []byte, 8)
	l, err := New(Options{ClientIP: netip.MustParseAddr("10.200.0.2"), Send: func(bond.QueuedPacket) bool { return true }, DeliverIP: func([]byte) {},
		LANWrite: func(f []byte) { out <- f }, DHCP: &DHCPConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	link := &testLink{l, out}
	p := &bond.GatewayProvisioning{Revision: 1, TopologyID: "t", SegmentID: "s", ExitID: "e", Mode: bond.GatewayPrimary, DHCPAuthorityID: "e",
		Link: bond.GatewayLinkProvisioning{ClientAddress: "10.77.0.2/24", ExitAddress: "10.77.0.1", MTU: 1400}}
	if err := l.Configure(p); err != nil {
		t.Fatal(err)
	}
	if _, consumed := l.HandleLANFrame(dhcpRequest(ps5MAC, 1, netip.Addr{}, netip.Addr{})); consumed {
		t.Fatal("with nothing delegated the exit must keep answering DHCP")
	}
	p.Revision = 2
	p.ClientHosts = []bond.GatewayClientHost{{MAC: "02:11:22:33:44:55", IP: "10.77.0.50", Name: "ps5"}}
	p.ClientDNS = []string{"9.9.9.9"}
	if err := l.Configure(p); err != nil {
		t.Fatal(err)
	}
	reply, consumed := link.exchange(t, dhcpRequest(ps5MAC, 1, netip.Addr{}, netip.Addr{}))
	if !consumed || reply == nil {
		t.Fatal("no OFFER for a delegated host")
	}
	ip, opts := dhcpOptions(reply)
	if ip != netip.MustParseAddr("10.77.0.50") || !bytes.Equal(opts[6], []byte{10, 77, 0, 2}) {
		t.Fatalf("OFFER ignored the provisioned host or DNS: %v %v", ip, opts)
	}
	p.Revision, p.ClientHosts, p.ClientDNS = 3, nil, nil
	if err := l.Configure(p); err != nil {
		t.Fatal(err)
	}
	if _, consumed := l.HandleLANFrame(dhcpRequest(ps5MAC, 1, netip.Addr{}, netip.Addr{})); consumed {
		t.Fatal("withdrawing the delegation must hand DHCP back to the exit")
	}
}

package gvisorlink

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"github.com/Dankular/GameBond.Core/bond"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/checksum"
	"gvisor.dev/gvisor/pkg/tcpip/header"
)

func udpIPv4(src, dst netip.Addr, sport, dport uint16, payload []byte) []byte {
	ip := make([]byte, 20+8+len(payload))
	ip[0], ip[8], ip[9] = 0x45, 64, 17
	binary.BigEndian.PutUint16(ip[2:], uint16(len(ip)))
	copy(ip[12:16], src.AsSlice())
	copy(ip[16:20], dst.AsSlice())
	binary.BigEndian.PutUint16(ip[10:], ipChecksum(ip[:20]))
	binary.BigEndian.PutUint16(ip[20:], sport)
	binary.BigEndian.PutUint16(ip[22:], dport)
	binary.BigEndian.PutUint16(ip[24:], uint16(8+len(payload)))
	copy(ip[28:], payload) // checksum 0: not verified for UDP/IPv4
	return ip
}

// The client stack is the NAT: an application packet leaves the bond NIC with the client's segment
// address as its source, and the reply to that address is translated back for the application.
func TestApplicationTrafficIsMasqueradedInsideTheStack(t *testing.T) {
	app := netip.MustParseAddr("10.77.0.2")
	sent := make(chan []byte, 8)
	delivered := make(chan []byte, 8)
	l, err := New(Options{ClientIP: app,
		Send: func(q bond.QueuedPacket) bool {
			if frame, ok := bond.UnwrapFrame(q.Payload); ok {
				sent <- append([]byte(nil), frame...)
			}
			return true
		},
		DeliverIP: func(ip []byte) { delivered <- append([]byte(nil), ip...) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	p := &bond.GatewayProvisioning{Revision: 1, TopologyID: "t", SegmentID: "s", ExitID: "e", Mode: bond.GatewayPrimary, DHCPAuthorityID: "e",
		Link: bond.GatewayLinkProvisioning{ClientAddress: "10.78.0.2/24", ExitAddress: "10.78.0.1", MTU: 1400}}
	if err := l.Configure(p); err != nil {
		t.Fatal(err)
	}
	if err := l.stack.AddStaticNeighbor(bondNIC, header.IPv4ProtocolNumber, tcpip.AddrFrom4([4]byte{10, 78, 0, 1}), tcpip.LinkAddress(string(peerMAC))); err != nil {
		t.Fatalf("neighbor: %s", err)
	}

	dns := netip.MustParseAddr("8.8.8.8")
	if !l.ForwardIP(bond.QueuedPacket{Payload: udpIPv4(app, dns, 40000, 53, []byte("query"))}) {
		t.Fatal("the link refused the application packet")
	}
	var out []byte
	select {
	case frame := <-sent:
		out = ethernetIPv4Payload(frame)
	case <-time.After(time.Second):
		t.Fatal("no frame left the bond NIC")
	}
	if out == nil || !bytes.Equal(out[12:16], []byte{10, 78, 0, 2}) || !bytes.Equal(out[16:20], dns.AsSlice()) {
		t.Fatalf("source must be the segment address 10.78.0.2, got %v -> %v", out[12:16], out[16:20])
	}
	natPort := binary.BigEndian.Uint16(out[20:22])

	reply := udpIPv4(dns, netip.MustParseAddr("10.78.0.2"), 53, natPort, []byte("answer"))
	frame := append(append(append([]byte(nil), bondMACBytes...), peerMAC...), 0x08, 0x00)
	if !l.ReceiveEthernet(append(frame, reply...)) {
		t.Log("reply was not claimed as client-stack traffic") // translated destination is checked below
	}
	select {
	case ip := <-delivered:
		if !bytes.Equal(ip[16:20], app.AsSlice()) || binary.BigEndian.Uint16(ip[22:24]) != 40000 || !bytes.Equal(ip[12:16], dns.AsSlice()) {
			t.Fatalf("reply was not translated back: dst %v:%d", ip[16:20], binary.BigEndian.Uint16(ip[22:24]))
		}
	case <-time.After(time.Second):
		t.Fatal("the translated reply never reached the application")
	}
}

func tcpIPv4(src, dst netip.Addr, sport, dport uint16, flags byte) []byte {
	ip := make([]byte, 40)
	ip[0], ip[8], ip[9] = 0x45, 64, 6
	binary.BigEndian.PutUint16(ip[2:], 40)
	copy(ip[12:16], src.AsSlice())
	copy(ip[16:20], dst.AsSlice())
	binary.BigEndian.PutUint16(ip[10:], ipChecksum(ip[:20]))
	binary.BigEndian.PutUint16(ip[20:], sport)
	binary.BigEndian.PutUint16(ip[22:], dport)
	ip[32], ip[33] = 5<<4, flags
	binary.BigEndian.PutUint16(ip[34:], 65535)
	pseudo := header.PseudoHeaderChecksum(header.TCPProtocolNumber, tcpip.AddrFrom4(src.As4()), tcpip.AddrFrom4(dst.As4()), 20)
	header.TCP(ip[20:]).SetChecksum(^checksum.Checksum(ip[20:], pseudo))
	return ip
}

func icmpEcho(src, dst netip.Addr, id uint16) []byte {
	ip := make([]byte, 28)
	ip[0], ip[8], ip[9] = 0x45, 64, 1
	binary.BigEndian.PutUint16(ip[2:], 28)
	copy(ip[12:16], src.AsSlice())
	copy(ip[16:20], dst.AsSlice())
	binary.BigEndian.PutUint16(ip[10:], ipChecksum(ip[:20]))
	ip[20] = 8
	binary.BigEndian.PutUint16(ip[24:], id)
	binary.BigEndian.PutUint16(ip[22:], ipChecksum(ip[20:]))
	return ip
}

// TCP and ICMP must be translated too (the first build only registered UDP and panicked on TCP).
func TestTCPAndICMPAreMasqueradedWithoutPanicking(t *testing.T) {
	app := netip.MustParseAddr("10.77.0.2")
	sent := make(chan []byte, 8)
	l, err := New(Options{ClientIP: app,
		Send: func(q bond.QueuedPacket) bool {
			if frame, ok := bond.UnwrapFrame(q.Payload); ok {
				sent <- append([]byte(nil), frame...)
			}
			return true
		},
		DeliverIP: func([]byte) {}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	p := &bond.GatewayProvisioning{Revision: 1, TopologyID: "t", SegmentID: "s", ExitID: "e", Mode: bond.GatewayPrimary, DHCPAuthorityID: "e",
		Link: bond.GatewayLinkProvisioning{ClientAddress: "10.78.0.2/24", ExitAddress: "10.78.0.1", MTU: 1400}}
	if err := l.Configure(p); err != nil {
		t.Fatal(err)
	}
	if err := l.stack.AddStaticNeighbor(bondNIC, header.IPv4ProtocolNumber, tcpip.AddrFrom4([4]byte{10, 78, 0, 1}), tcpip.LinkAddress(string(peerMAC))); err != nil {
		t.Fatalf("neighbor: %s", err)
	}
	dst := netip.MustParseAddr("1.1.1.1")
	for name, packet := range map[string][]byte{"tcp": tcpIPv4(app, dst, 50000, 443, 0x02), "icmp": icmpEcho(app, dst, 7)} {
		if !l.ForwardIP(bond.QueuedPacket{Payload: packet}) {
			t.Fatalf("%s: the link refused the packet", name)
		}
		select {
		case frame := <-sent:
			out := ethernetIPv4Payload(frame)
			if out == nil || !bytes.Equal(out[12:16], []byte{10, 78, 0, 2}) {
				t.Fatalf("%s: source must be the segment address, got %v", name, out)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s: no frame left the bond NIC", name)
		}
	}
}

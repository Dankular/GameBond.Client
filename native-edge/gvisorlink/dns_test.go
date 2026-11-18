package gvisorlink

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"
)

func arpRequest(mac [6]byte, sender, target netip.Addr) []byte {
	f := append([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, mac[:]...)
	f = append(f, 0x08, 0x06, 0, 1, 8, 0, 6, 4, 0, 1)
	f = append(f, mac[:]...)
	f = append(f, sender.AsSlice()...)
	f = append(f, make([]byte, 6)...)
	return append(f, target.AsSlice()...)
}

func dnsQuery(mac [6]byte, src, dst netip.Addr, id uint16) []byte {
	q := []byte{byte(id >> 8), byte(id), 0x01, 0, 0, 1, 0, 0, 0, 0, 0, 0, 3, 'p', 's', '5', 0, 0, 1, 0, 1}
	ip := udpIPv4(src, dst, 40001, 53, q)
	f := append(append([]byte(nil), lanMACBytes...), mac[:]...)
	return append(append(f, 0x08, 0x00), ip...)
}

// The device finds its DNS server (the client address) by ARP, sends the query to the virtual LAN NIC,
// and the answer from the upstream resolver comes back to it through the same NIC.
func TestClientAnswersDNSForTheBridgedDevice(t *testing.T) {
	upstream, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := upstream.ReadFrom(buf)
			if err != nil {
				return
			}
			reply := append([]byte(nil), buf[:n]...)
			reply[2], reply[3] = 0x81, 0x80
			reply = append(reply, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 93, 184, 216, 34)
			reply[7] = 1
			_, _ = upstream.WriteTo(reply, from)
		}
	}()
	old := dnsPort
	_, dnsPort, _ = net.SplitHostPort(upstream.LocalAddr().String())
	t.Cleanup(func() { dnsPort = old })

	l := newDHCPLink(t)
	l.dhcpCfg.DNS = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	provision(t, l.Link)
	client, device := netip.MustParseAddr("10.77.0.2"), netip.MustParseAddr("10.77.0.50")

	reply, consumed := l.exchange(t, arpRequest(ps5MAC, device, client))
	if !consumed || reply == nil || binary.BigEndian.Uint16(reply[12:14]) != 0x0806 || !bytes.Equal(reply[6:12], lanMACBytes) || reply[21] != 2 {
		t.Fatalf("the virtual LAN NIC must answer ARP for the client address: consumed=%v % x", consumed, reply)
	}
	reply, consumed = l.exchange(t, dnsQuery(ps5MAC, device, client, 0x1234))
	if !consumed {
		t.Fatal("the DNS query was not consumed by the stack")
	}
	deadline := time.After(3 * time.Second)
	for reply == nil || binary.BigEndian.Uint16(reply[12:14]) != 0x0800 {
		select {
		case reply = <-l.out:
		case <-deadline:
			t.Fatal("no DNS answer reached the device")
		}
	}
	ip := reply[14:]
	udpHdr := ip[(ip[0]&0x0f)*4:]
	if !bytes.Equal(reply[0:6], ps5MAC[:]) || !bytes.Equal(ip[12:16], client.AsSlice()) || !bytes.Equal(ip[16:20], device.AsSlice()) ||
		binary.BigEndian.Uint16(udpHdr[0:2]) != 53 || binary.BigEndian.Uint16(udpHdr[2:4]) != 40001 {
		t.Fatalf("answer must go from client:53 to the device: % x", reply[:40])
	}
	body := udpHdr[8:]
	if body[0] != 0x12 || body[1] != 0x34 || body[2]&0x80 == 0 || !bytes.HasSuffix(body, []byte{93, 184, 216, 34}) {
		t.Fatalf("unexpected DNS answer: % x", body)
	}
}

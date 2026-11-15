package lan

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/Dankular/GameBond.Core/bond"
)

func l2frame(dst, src byte, et uint16, n int) []byte {
	f := []byte{2, 0, 0, 0, 0, dst, 2, 0, 0, 0, 0, src, byte(et >> 8), byte(et)}
	return append(f, make([]byte, n)...)
}

func newTestL2Bridge(t *testing.T, cfg L2BridgeConfig) (*L2Bridge, *MemoryIO, *[]bond.QueuedPacket) {
	t.Helper()
	adapter, wire := MemoryPair([6]byte{2, 0, 0, 0, 0, 0xaa}, [6]byte{2, 0, 0, 0, 0, 0xbb})
	_ = adapter
	cl, err := bond.NewClassifier(nil, nil, bond.DefaultQoSSettings())
	if err != nil {
		t.Fatal(err)
	}
	var queued []bond.QueuedPacket
	b := NewL2Bridge(cfg, wire, cl, func(q bond.QueuedPacket) bool { queued = append(queued, q); return true })
	return b, adapter, &queued
}

func TestL2BridgeCarriesDeviceFramesIntoTheBondAndBack(t *testing.T) {
	b, peer, queued := newTestL2Bridge(t, L2BridgeConfig{Name: "ps5"})
	arp := l2frame(0xff, 0x10, 0x0806, 28)
	arp[0], arp[1], arp[2], arp[3], arp[4], arp[5] = 0xff, 0xff, 0xff, 0xff, 0xff, 0xff
	b.FromAdapter(arp)
	if len(*queued) != 1 || !(*queued)[0].Priority || !(*queued)[0].Replicate || (*queued)[0].Tag != 46 {
		t.Fatalf("ARP must go as a priority copy: %+v", *queued)
	}
	if p := (*queued)[0].Payload; p[0] != bond.L2Marker || !bytes.Equal(p[1:], arp) {
		t.Fatal("payload is not the wrapped frame")
	}
	reply := l2frame(0x10, 0x20, 0x88b5, 200)
	if !b.Deliver(bond.WrapFrame(reply)) {
		t.Fatal("L2 payload not recognised")
	}
	if got, _ := peer.Read(50 * time.Millisecond); !bytes.Equal(got, reply) {
		t.Fatal("device did not receive the frame")
	}
	runt := l2frame(0x10, 0x20, 0x0806, 28)
	b.Deliver(bond.WrapFrame(runt))
	if got, _ := peer.Read(50 * time.Millisecond); len(got) != minWireFrame || !bytes.Equal(got[:len(runt)], runt) {
		t.Fatalf("runt frame must reach the adapter padded to %d, got %d", minWireFrame, len(got))
	}
	if b.Deliver([]byte{0x45, 0, 0, 20}) {
		t.Fatal("an IPv4 packet must not be taken as L2")
	}
}

func TestL2BridgeCountsOversizeAndAppliesMACCap(t *testing.T) {
	b, _, queued := newTestL2Bridge(t, L2BridgeConfig{Name: "ps5", MACLimit: 1})
	b.FromAdapter(l2frame(0x30, 0x10, 0x88b5, 1600))
	if len(*queued) != 0 || b.Stats()["oversize"].(uint64) != 1 {
		t.Fatal("oversize frame must be counted and not queued")
	}
	b.FromAdapter(l2frame(0x30, 0x10, 0x88b5, 60))
	b.FromAdapter(l2frame(0x30, 0x11, 0x88b5, 60)) // a second source MAC behind a port capped at one
	st := b.Stats()
	if len(*queued) != 1 || st["drop_mac_limit"].(uint64) != 1 {
		t.Fatalf("MAC cap not enforced: queued=%d stats=%v", len(*queued), st)
	}
}

func TestL2BridgeCountsAndRejectsOversizeTunnelFrames(t *testing.T) {
	b, peer, _ := newTestL2Bridge(t, L2BridgeConfig{Name: "ps5", FrameMTU: 600})
	frame := l2frame(0x10, 0x20, 0x88b5, 587) // 601 bytes including Ethernet header.
	if !b.Deliver(bond.WrapFrame(frame)) {
		t.Fatal("oversize Ethernet payload was not recognized as L2")
	}
	if got := b.Stats()["oversize"].(uint64); got != 1 {
		t.Fatalf("oversize count=%d, want 1", got)
	}
	if got, _ := peer.Read(10 * time.Millisecond); len(got) != 0 {
		t.Fatalf("oversize frame reached the adapter: len=%d", len(got))
	}
}

func TestL2BridgeFrameMTUReservesMaximumEgressTagStack(t *testing.T) {
	for _, test := range []struct {
		name        string
		tunnel, lan bond.L2VLANPolicy
		want        int
	}{
		{name: "qinq service trunk", tunnel: bond.L2VLANPolicy{ServiceVLAN: 100, AllowedCustomerVLANs: []uint16{10, 20}}, want: bond.DefaultL2FrameMTU + 8},
		{name: "qinq customer access", tunnel: bond.L2VLANPolicy{ServiceVLAN: 100, CustomerVLAN: 10}, want: bond.DefaultL2FrameMTU},
		{name: "single tag trunk", lan: bond.L2VLANPolicy{AllowedVLANs: []uint16{10}}, want: bond.DefaultL2FrameMTU + 4},
		{name: "maximum across ports", tunnel: bond.L2VLANPolicy{ServiceVLAN: 100, AllowedCustomerVLANs: []uint16{10}}, lan: bond.L2VLANPolicy{AllowedVLANs: []uint16{20}}, want: bond.DefaultL2FrameMTU + 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			b, _, _ := newTestL2Bridge(t, L2BridgeConfig{Name: "qinq", TunnelVLAN: test.tunnel, LANVLAN: test.lan})
			if b.cfg.FrameMTU != test.want {
				t.Fatalf("FrameMTU=%d, want %d", b.cfg.FrameMTU, test.want)
			}
		})
	}
}

func TestL2BridgeAllowsReservedSingleAndQinQTagGrowth(t *testing.T) {
	for _, test := range []struct {
		name        string
		tunnel, lan bond.L2VLANPolicy
		wantDepth   int
	}{
		{name: "single tag", tunnel: bond.L2VLANPolicy{AccessVLAN: 10}, lan: bond.L2VLANPolicy{AllowedVLANs: []uint16{10}}, wantDepth: 1},
		{name: "qinq", tunnel: bond.L2VLANPolicy{ServiceVLAN: 100, CustomerVLAN: 10}, lan: bond.L2VLANPolicy{ServiceVLAN: 100, AllowedCustomerVLANs: []uint16{10}}, wantDepth: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			b, peer, _ := newTestL2Bridge(t, L2BridgeConfig{Name: "vlan", FrameMTU: 600, TunnelVLAN: test.tunnel, LANVLAN: test.lan})
			frame := l2frame(0x10, 0x20, 0x88b5, 586) // exactly the configured untagged frame limit.
			if len(frame) != 600 || !b.Deliver(bond.WrapFrame(frame)) {
				t.Fatal("base-MTU tunnel frame was not accepted as L2")
			}
			got, _ := peer.Read(50 * time.Millisecond)
			wantLen := 600 + 4*test.wantDepth
			if len(got) != wantLen {
				t.Fatalf("tagged adapter frame length=%d, want %d", len(got), wantLen)
			}
			if test.wantDepth == 1 && (got[12] != 0x81 || got[13] != 0x00) {
				t.Fatalf("single-tag egress TPID=%02x%02x", got[12], got[13])
			}
			if test.wantDepth == 2 && (got[12] != 0x88 || got[13] != 0xa8 || got[16] != 0x81 || got[17] != 0x00) {
				t.Fatalf("Q-in-Q egress TPIDs=%02x%02x/%02x%02x", got[12], got[13], got[16], got[17])
			}
		})
	}
}

func TestL2BridgeCountsTagGrowthThatExceedsReservedMTU(t *testing.T) {
	const baseMTU = 600
	qinqAccess := bond.L2VLANPolicy{ServiceVLAN: 100, CustomerVLAN: 10}
	qinqTrunk := bond.L2VLANPolicy{ServiceVLAN: 100, AllowedCustomerVLANs: []uint16{10}}
	frame := l2frame(0x10, 0x20, 0x88b5, baseMTU+8-14)

	t.Run("adapter to tunnel", func(t *testing.T) {
		b, _, queued := newTestL2Bridge(t, L2BridgeConfig{Name: "qinq", FrameMTU: baseMTU, TunnelVLAN: qinqTrunk, LANVLAN: qinqAccess})
		b.FromAdapter(frame)
		if len(*queued) != 0 || b.Stats()["oversize"].(uint64) != 1 {
			t.Fatalf("tag-expanded frame must be counted and withheld: queued=%d stats=%v", len(*queued), b.Stats())
		}
	})
	t.Run("tunnel to adapter", func(t *testing.T) {
		b, peer, _ := newTestL2Bridge(t, L2BridgeConfig{Name: "qinq", FrameMTU: baseMTU, TunnelVLAN: qinqAccess, LANVLAN: qinqTrunk})
		if !b.Deliver(bond.WrapFrame(frame)) {
			t.Fatal("oversize Ethernet payload was not recognized as L2")
		}
		if b.Stats()["oversize"].(uint64) != 1 {
			t.Fatalf("tag-expanded frame was not counted: %v", b.Stats())
		}
		if got, _ := peer.Read(10 * time.Millisecond); len(got) != 0 {
			t.Fatalf("tag-expanded frame reached the adapter: len=%d", len(got))
		}
	})
}

func TestL2BridgeFragmentsIPv4ToFitQinQTunnelEgress(t *testing.T) {
	qinqAccess := bond.L2VLANPolicy{ServiceVLAN: 100, CustomerVLAN: 10}
	qinqTrunk := bond.L2VLANPolicy{ServiceVLAN: 100, AllowedCustomerVLANs: []uint16{10}}
	b, _, queued := newTestL2Bridge(t, L2BridgeConfig{Name: "qinq", FrameMTU: 1394, TunnelVLAN: qinqTrunk, LANVLAN: qinqAccess})
	frame := make([]byte, 14+1500)
	copy(frame[:6], []byte{2, 0, 0, 0, 0, 2})
	copy(frame[6:12], []byte{2, 0, 0, 0, 0, 1})
	frame[12], frame[13] = 0x08, 0x00
	ip := frame[14:]
	ip[0], ip[8], ip[9] = 0x45, 64, 17
	binary.BigEndian.PutUint16(ip[2:4], uint16(len(ip)))
	copy(ip[12:16], []byte{192, 0, 2, 1})
	copy(ip[16:20], []byte{198, 51, 100, 2})
	for i := 20; i < len(ip); i++ {
		ip[i] = byte(i)
	}

	b.FromAdapter(frame)
	if len(*queued) != 2 {
		t.Fatalf("oversize IPv4 should be fragmented before Q-in-Q egress: queued=%d stats=%v", len(*queued), b.Stats())
	}
	for i, packet := range *queued {
		out, ok := bond.UnwrapFrame(packet.Payload)
		if !ok || len(out) > 1402 {
			t.Fatalf("fragment %d invalid or exceeds reserved transport MTU: len=%d", i, len(out))
		}
		info, err := bond.ParseFrame(out)
		if err != nil || info.VLANDepth != 2 || info.EtherType != 0x0800 {
			t.Fatalf("fragment %d lost Q-in-Q/IPv4 framing: info=%+v err=%v", i, info, err)
		}
	}
}

func TestOversizeIPv4IsFragmentedOrAnsweredWithPMTU(t *testing.T) {
	mk := func(df bool, payload int) []byte {
		f := make([]byte, 14+20+payload)
		copy(f[0:6], []byte{2, 0x47, 0x42, 0, 0, 1})
		copy(f[6:12], []byte{0xc0, 0x15, 0x1b, 0xb2, 0x80, 0xac})
		f[12], f[13] = 0x08, 0
		ip := f[14:]
		ip[0] = 0x45
		binary.BigEndian.PutUint16(ip[2:], uint16(20+payload))
		if df {
			ip[6] = 0x40
		}
		ip[8], ip[9] = 64, 17
		copy(ip[12:16], []byte{10, 78, 0, 10})
		copy(ip[16:20], []byte{8, 8, 8, 8})
		for i := 0; i < payload; i++ {
			ip[20+i] = byte(i)
		}
		return f
	}
	fwd, back, ok := oversizeIPv4(mk(true, 1480), 1394)
	if !ok || len(fwd) != 0 || len(back) != 1 || back[0][14+20] != 3 || back[0][14+21] != 4 ||
		binary.BigEndian.Uint16(back[0][14+20+6:]) != 1380 || ipChecksum(back[0][14+20:]) != 0 {
		t.Fatalf("DF packet must be answered with fragmentation needed: %v %v %v", ok, len(fwd), len(back))
	}
	fwd, back, ok = oversizeIPv4(mk(false, 1480), 1394)
	if !ok || len(back) != 0 || len(fwd) != 2 {
		t.Fatalf("non-DF packet must be fragmented: %v %d %d", ok, len(fwd), len(back))
	}
	got := 0
	for i, f := range fwd {
		if len(f) > 1394 || ipChecksum(f[14:34]) != 0 {
			t.Fatalf("fragment %d is %d bytes or has a bad checksum", i, len(f))
		}
		fl := binary.BigEndian.Uint16(f[14+6:])
		if (i == 0) != (fl&0x2000 != 0) || int(fl&0x1fff) != got/8 {
			t.Fatalf("fragment %d flags %#x at %d", i, fl, got)
		}
		got += len(f) - 34
	}
	if got != 1480 {
		t.Fatalf("fragments carry %d bytes", got)
	}
	if _, _, ok := oversizeIPv4(make([]byte, 1600), 1394); ok {
		t.Fatal("a non-IPv4 oversize frame must stay dropped")
	}
	withTags := func(frame []byte, tpids ...uint16) []byte {
		out := append([]byte(nil), frame[:12]...)
		for i, tpid := range tpids {
			out = append(out, byte(tpid>>8), byte(tpid), 0, byte(i+1))
			if i == len(tpids)-1 {
				out = append(out, frame[12], frame[13])
			}
		}
		return append(out, frame[14:]...)
	}
	for _, test := range []struct {
		name string
		tpid []uint16
	}{
		{name: "single tag", tpid: []uint16{0x8100}},
		{name: "qinq", tpid: []uint16{0x88a8, 0x8100}},
	} {
		t.Run(test.name, func(t *testing.T) {
			frameMTU := 1394 + 4*len(test.tpid)
			taggedDF := withTags(mk(true, 1480), test.tpid...)
			fwd, back, ok := oversizeIPv4(taggedDF, frameMTU)
			ipOffset := 14 + 4*len(test.tpid)
			if !ok || len(fwd) != 0 || len(back) != 1 || !bytes.Equal(back[0][12:ipOffset], taggedDF[12:ipOffset]) ||
				!bytes.Equal(back[0][:6], taggedDF[6:12]) || !bytes.Equal(back[0][6:12], taggedDF[:6]) ||
				binary.BigEndian.Uint16(back[0][ipOffset+20+6:]) != 1380 || ipChecksum(back[0][ipOffset:ipOffset+20]) != 0 ||
				ipChecksum(back[0][ipOffset+20:]) != 0 {
				t.Fatalf("tagged DF packet did not preserve tags and return a valid PMTU response: ok=%v fwd=%d back=%d", ok, len(fwd), len(back))
			}
			tagged := withTags(mk(false, 1480), test.tpid...)
			fwd, back, ok = oversizeIPv4(tagged, frameMTU)
			if !ok || len(back) != 0 || len(fwd) != 2 {
				t.Fatalf("tagged non-DF packet was not fragmented: ok=%v fwd=%d back=%d", ok, len(fwd), len(back))
			}
			for i, fragment := range fwd {
				if len(fragment) > frameMTU || !bytes.Equal(fragment[:ipOffset], tagged[:ipOffset]) || ipChecksum(fragment[ipOffset:ipOffset+20]) != 0 {
					t.Fatalf("fragment %d exceeded MTU, lost tags, or has a bad checksum", i)
				}
			}
		})
	}
}

package lan

import (
	"encoding/binary"
	"bytes"
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
}

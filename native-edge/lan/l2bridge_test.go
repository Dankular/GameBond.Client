package lan

import (
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
	reply := l2frame(0x10, 0x20, 0x88b5, 100)
	if !b.Deliver(bond.WrapFrame(reply)) {
		t.Fatal("L2 payload not recognised")
	}
	if got, _ := peer.Read(50 * time.Millisecond); !bytes.Equal(got, reply) {
		t.Fatal("device did not receive the frame")
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

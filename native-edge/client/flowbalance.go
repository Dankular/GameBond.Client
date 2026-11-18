package client

import (
	"bytes"
	"encoding/binary"
	"math"
	"net/netip"
	"time"

	"github.com/Dankular/GameBond.Core/bond"
)

const (
	maxTrackedExitFlows = 262144
	tcpFlowIdle         = 2 * time.Hour
	udpFlowIdle         = 5 * time.Minute
	otherFlowIdle       = 30 * time.Second
	tcpCloseGrace       = 30 * time.Second
	tcpResetGrace       = 3 * time.Second
)

type flowExit struct {
	addr     netip.AddrPort
	capacity float64
	rtt      float64
	hops     int
	ready    bool // eligible for new flows
	retain   bool // keep existing sessions pinned while this stack recovers
}

type flowSession struct {
	exit       netip.AddrPort
	protocol   byte
	lastSeen   time.Time
	closeUntil time.Time
	finMask    uint8
}

type flowExitCounters struct {
	sessions      uint64
	txPackets     uint64
	txBytes       uint64
	rxPackets     uint64
	rxBytes       uint64
	reassignments uint64
	untracked     uint64
	windowBytes   uint64
	lastBytes     uint64
	lastRateAt    time.Time
	rateMbps      float64
}

type flowRouteTable struct {
	sessions map[string]flowSession
	perExit  map[netip.AddrPort]flowExitCounters
}

type flowRouteStats struct {
	Sessions      uint64
	TXPackets     uint64
	TXBytes       uint64
	RXPackets     uint64
	RXBytes       uint64
	MbpsOffered   float64
	Reassignments uint64
	Untracked     uint64
}

type flowPacketInfo struct {
	key      string
	protocol byte
	tcpFlags byte
	reverse  bool
}

func newFlowRouteTable() *flowRouteTable {
	return &flowRouteTable{sessions: make(map[string]flowSession), perExit: make(map[netip.AddrPort]flowExitCounters)}
}

func (t *flowRouteTable) choose(exits []flowExit) netip.AddrPort {
	if len(exits) == 0 {
		return netip.AddrPort{}
	}
	best := exits[0]
	bestLoad := flowExitLoad(t.perExit[best.addr], best)
	found := best.ready
	for _, e := range exits[1:] {
		if !e.ready {
			continue
		}
		load := flowExitLoad(t.perExit[e.addr], e)
		if !found || load < bestLoad {
			best, bestLoad = e, load
			found = true
		}
	}
	if !found {
		return netip.AddrPort{}
	}
	return best.addr
}

func flowExitLoad(c flowExitCounters, e flowExit) float64 {
	// Byte rate captures elephant flows; a small per-session floor spreads new,
	// still-idle flows before their first rate sample is available.
	estimatedMbps := c.rateMbps + float64(c.sessions)*0.05
	return estimatedMbps / flowExitWeight(e)
}

func chooseOverflowFlow(key string, exits []flowExit) netip.AddrPort {
	best := netip.AddrPort{}
	bestScore := math.Inf(1)
	for _, exit := range exits {
		if !exit.ready {
			continue
		}
		hash := uint64(14695981039346656037)
		for i := 0; i < len(key); i++ {
			hash ^= uint64(key[i])
			hash *= 1099511628211
		}
		addr := exit.addr.Addr().As4()
		for _, b := range addr {
			hash ^= uint64(b)
			hash *= 1099511628211
		}
		port := exit.addr.Port()
		hash ^= uint64(port >> 8)
		hash *= 1099511628211
		hash ^= uint64(byte(port))
		hash *= 1099511628211
		u := (float64(hash>>11) + 1) / (float64(uint64(1)<<53) + 1)
		score := -math.Log(u) / flowExitWeight(exit)
		if score < bestScore {
			best, bestScore = exit.addr, score
		}
	}
	return best
}

func flowExitWeight(e flowExit) float64 {
	weight := e.capacity
	if weight <= 0 {
		weight = 1
	}
	if e.rtt > 0 {
		weight /= 1 + e.rtt/100
	}
	if e.hops > 1 {
		weight /= 1 + 0.02*float64(e.hops-1)
	}
	if weight < 0.01 {
		return 0.01
	}
	return weight
}

func (t *flowRouteTable) route(info flowPacketInfo, payloadBytes int, exits []flowExit, fallback netip.AddrPort, now time.Time) netip.AddrPort {
	if info.key == "" {
		return fallback
	}
	entry, exists := t.sessions[info.key]
	if exists && flowExpired(entry, now) {
		t.remove(info.key, entry)
		exists = false
	}
	available := false
	for _, e := range exits {
		if e.addr == entry.exit && e.retain {
			available = true
			break
		}
	}
	if exists {
		if available {
			entry.lastSeen = now
			entry.protocol = info.protocol
			observeTCPState(&entry, info, now)
			t.sessions[info.key] = entry
			t.notePacket(entry.exit, payloadBytes, now)
			return entry.exit
		}
		t.remove(info.key, entry)
		counters := t.perExit[entry.exit]
		counters.reassignments++
		t.perExit[entry.exit] = counters
	}
	selected := t.choose(exits)
	if !selected.IsValid() {
		return fallback
	}
	if len(t.sessions) >= maxTrackedExitFlows {
		selected = chooseOverflowFlow(info.key, exits)
		if !selected.IsValid() {
			return fallback
		}
		counters := t.perExit[selected]
		counters.untracked++
		t.perExit[selected] = counters
		t.notePacket(selected, payloadBytes, now)
		return selected
	}
	entry = flowSession{exit: selected, protocol: info.protocol, lastSeen: now}
	observeTCPState(&entry, info, now)
	t.sessions[info.key] = entry
	counters := t.perExit[selected]
	counters.sessions++
	t.perExit[selected] = counters
	t.notePacket(selected, payloadBytes, now)
	return selected
}

func (t *flowRouteTable) notePacket(exit netip.AddrPort, size int, now time.Time) {
	counters := t.perExit[exit]
	counters.txPackets++
	if size > 0 {
		counters.txBytes += uint64(size)
		counters.windowBytes += uint64(size)
	}
	if counters.lastRateAt.IsZero() {
		counters.lastRateAt = now
		counters.lastBytes = counters.windowBytes
	}
	t.perExit[exit] = counters
}

func (t *flowRouteTable) observeInbound(info flowPacketInfo, exit netip.AddrPort, size int, now time.Time) {
	entry, ok := t.sessions[info.key]
	if !ok || entry.exit != exit || flowExpired(entry, now) {
		return
	}
	entry.lastSeen = now
	observeTCPState(&entry, info, now)
	t.sessions[info.key] = entry
	counters := t.perExit[exit]
	counters.rxPackets++
	if size > 0 {
		counters.rxBytes += uint64(size)
		counters.windowBytes += uint64(size)
	}
	t.perExit[exit] = counters
}

func observeTCPState(entry *flowSession, info flowPacketInfo, now time.Time) {
	if entry.protocol != 6 {
		return
	}
	if info.tcpFlags&0x04 != 0 {
		entry.closeUntil = now.Add(tcpResetGrace)
		return
	}
	if info.tcpFlags&0x01 == 0 {
		return
	}
	if info.reverse {
		entry.finMask |= 2
	} else {
		entry.finMask |= 1
	}
	if entry.finMask == 3 {
		entry.closeUntil = now.Add(tcpCloseGrace)
	}
}

func (t *flowRouteTable) remove(key string, entry flowSession) {
	delete(t.sessions, key)
	counters := t.perExit[entry.exit]
	if counters.sessions > 0 {
		counters.sessions--
	}
	t.perExit[entry.exit] = counters
}

func flowExpired(entry flowSession, now time.Time) bool {
	if !entry.closeUntil.IsZero() {
		return !now.Before(entry.closeUntil)
	}
	idle := otherFlowIdle
	switch entry.protocol {
	case 6:
		idle = tcpFlowIdle
	case 17:
		idle = udpFlowIdle
	}
	return now.Sub(entry.lastSeen) >= idle
}

func (t *flowRouteTable) expire(now time.Time) {
	for key, entry := range t.sessions {
		if flowExpired(entry, now) {
			t.remove(key, entry)
		}
	}
	for exit, counters := range t.perExit {
		if counters.lastRateAt.IsZero() {
			counters.lastRateAt = now
			counters.lastBytes = counters.windowBytes
		} else if elapsed := now.Sub(counters.lastRateAt); elapsed >= time.Second {
			var delta uint64
			if counters.windowBytes >= counters.lastBytes {
				delta = counters.windowBytes - counters.lastBytes
			}
			rate := float64(delta) * 8 / elapsed.Seconds() / 1_000_000
			if counters.rateMbps == 0 {
				counters.rateMbps = rate
			} else {
				counters.rateMbps += 0.35 * (rate - counters.rateMbps)
			}
			counters.lastBytes, counters.lastRateAt = counters.windowBytes, now
		}
		t.perExit[exit] = counters
	}
}

func (t *flowRouteTable) stats(exit netip.AddrPort) flowRouteStats {
	c := t.perExit[exit]
	return flowRouteStats{Sessions: c.sessions, TXPackets: c.txPackets, TXBytes: c.txBytes, RXPackets: c.rxPackets, RXBytes: c.rxBytes, MbpsOffered: c.rateMbps, Reassignments: c.reassignments, Untracked: c.untracked}
}

func packetFlowInfo(packet []byte) (flowPacketInfo, bool) {
	if len(packet) < 1 || packet[0] == bond.L2Marker {
		return flowPacketInfo{}, false
	}
	switch packet[0] >> 4 {
	case 4:
		if len(packet) < 20 {
			return flowPacketInfo{}, false
		}
		header := int(packet[0]&15) * 4
		if header < 20 || header > len(packet) || int(binary.BigEndian.Uint16(packet[2:4])) != len(packet) {
			return flowPacketInfo{}, false
		}
		protocol := packet[9]
		fragment := binary.BigEndian.Uint16(packet[6:8])
		fragmentOffset := int(fragment&0x1fff) * 8
		var src, dst [4]byte
		copy(src[:], packet[12:16])
		copy(dst[:], packet[16:20])
		key := ""
		reverse := false
		if fragment&0x3fff != 0 {
			keyBytes := make([]byte, 11)
			keyBytes[0] = protocol
			if bytes.Compare(src[:], dst[:]) <= 0 {
				copy(keyBytes[1:5], src[:])
				copy(keyBytes[5:9], dst[:])
			} else {
				reverse = true
				copy(keyBytes[1:5], dst[:])
				copy(keyBytes[5:9], src[:])
			}
			copy(keyBytes[9:], packet[4:6])
			key = string(keyBytes)
		} else {
			var sport, dport uint16
			if protocol == 6 || protocol == 17 {
				need := header + 4
				if protocol == 6 {
					need = header + 20
				}
				if need > len(packet) {
					return flowPacketInfo{}, false
				}
				sport = binary.BigEndian.Uint16(packet[header : header+2])
				dport = binary.BigEndian.Uint16(packet[header+2 : header+4])
			}
			key, reverse = ipv4TupleFlow(protocol, src, sport, dst, dport)
		}
		flags := byte(0)
		if protocol == 6 && fragmentOffset == 0 && header+14 <= len(packet) {
			flags = packet[header+13]
		}
		return flowPacketInfo{key: key, protocol: protocol, tcpFlags: flags, reverse: reverse}, key != ""
	case 6:
		return ipv6FlowInfo(packet)
	default:
		return flowPacketInfo{}, false
	}
}

func ipv4TupleFlow(protocol byte, src [4]byte, sport uint16, dst [4]byte, dport uint16) (string, bool) {
	var a, b [6]byte
	copy(a[:4], src[:])
	binary.BigEndian.PutUint16(a[4:], sport)
	copy(b[:4], dst[:])
	binary.BigEndian.PutUint16(b[4:], dport)
	reverse := bytes.Compare(a[:], b[:]) > 0
	if reverse {
		a, b = b, a
	}
	key := make([]byte, 13)
	key[0] = protocol
	copy(key[1:7], a[:])
	copy(key[7:13], b[:])
	return string(key), reverse
}

func ipv6FlowInfo(packet []byte) (flowPacketInfo, bool) {
	if len(packet) < 40 || packet[0]>>4 != 6 {
		return flowPacketInfo{}, false
	}
	payloadLen := int(binary.BigEndian.Uint16(packet[4:6]))
	if payloadLen == 0 || payloadLen > len(packet)-40 {
		return flowPacketInfo{}, false
	}
	var src, dst [16]byte
	copy(src[:], packet[8:24])
	copy(dst[:], packet[24:40])
	next := packet[6]
	offset, end := 40, 40+payloadLen
	for steps := 0; steps < 8; steps++ {
		switch next {
		case 0, 43, 60, 135:
			if offset+2 > end {
				return flowPacketInfo{}, false
			}
			length := (int(packet[offset+1]) + 1) * 8
			if length < 8 || offset+length > end {
				return flowPacketInfo{}, false
			}
			next, offset = packet[offset], offset+length
		case 51:
			if offset+2 > end {
				return flowPacketInfo{}, false
			}
			length := (int(packet[offset+1]) + 2) * 4
			if length < 8 || offset+length > end {
				return flowPacketInfo{}, false
			}
			next, offset = packet[offset], offset+length
		case 44:
			if offset+8 > end {
				return flowPacketInfo{}, false
			}
			key := make([]byte, 39)
			key[0], key[1], key[2] = 6, 44, packet[offset]
			reverse := bytes.Compare(src[:], dst[:]) > 0
			if !reverse {
				copy(key[3:19], src[:])
				copy(key[19:35], dst[:])
			} else {
				copy(key[3:19], dst[:])
				copy(key[19:35], src[:])
			}
			copy(key[35:39], packet[offset+4:offset+8])
			return flowPacketInfo{key: string(key), protocol: packet[offset], reverse: reverse}, true
		default:
			return ipv6TransportFlow(packet, src, dst, next, offset, end)
		}
	}
	return flowPacketInfo{}, false
}

func ipv6TransportFlow(packet []byte, src, dst [16]byte, protocol byte, offset, end int) (flowPacketInfo, bool) {
	var a, b [18]byte
	copy(a[:16], src[:])
	copy(b[:16], dst[:])
	flags := byte(0)
	if protocol == 6 || protocol == 17 {
		need := 4
		if protocol == 6 {
			need = 14
		}
		if offset+need > end {
			return flowPacketInfo{}, false
		}
		copy(a[16:], packet[offset:offset+2])
		copy(b[16:], packet[offset+2:offset+4])
		if protocol == 6 {
			flags = packet[offset+13]
		}
	}
	reverse := bytes.Compare(a[:], b[:]) > 0
	if reverse {
		a, b = b, a
	}
	key := make([]byte, 38)
	key[0], key[1] = 6, protocol
	copy(key[2:20], a[:])
	copy(key[20:38], b[:])
	return flowPacketInfo{key: string(key), protocol: protocol, tcpFlags: flags, reverse: reverse}, true
}

package lan

import "encoding/binary"

// oversizeIPv4 handles an IPv4 frame larger than the tunnel can carry the way a router does: with
// DF set the device is told the usable MTU (ICMP fragmentation needed), so path-MTU discovery
// shrinks its segments; without DF the packet is split into fragments that fit. It preserves up
// to two VLAN tags and reports frames to send on toward the tunnel and back to the device. ok is
// false for anything other than a supported IPv4 frame, which stays dropped.
func oversizeIPv4(frame []byte, frameMTU int) (forward, back [][]byte, ok bool) {
	ipOffset, ok := ipv4Offset(frame)
	if !ok || len(frame) < ipOffset+20 || frame[ipOffset]>>4 != 4 {
		return nil, nil, false
	}
	ihl := int(frame[ipOffset]&0x0f) * 4
	total := int(binary.BigEndian.Uint16(frame[ipOffset+2:]))
	if ihl < 20 || total < ihl || ipOffset+total > len(frame) {
		return nil, nil, false
	}
	mtu := frameMTU - ipOffset
	if mtu < ihl+8 {
		return nil, nil, false
	}
	flags := binary.BigEndian.Uint16(frame[ipOffset+6:])
	if flags&0x4000 != 0 { // DF
		return nil, [][]byte{icmpFragNeeded(frame, ipOffset, ihl, mtu)}, true
	}
	payload := frame[ipOffset+ihl : ipOffset+total]
	baseOff := int(flags & 0x1fff)
	moreFrags := flags&0x2000 != 0
	step := (mtu - ihl) &^ 7
	for off := 0; off < len(payload); off += step {
		end := off + step
		last := end >= len(payload)
		if last {
			end = len(payload)
		}
		out := make([]byte, ipOffset+ihl+end-off)
		copy(out, frame[:ipOffset+ihl])
		copy(out[ipOffset+ihl:], payload[off:end])
		ip := out[ipOffset:]
		binary.BigEndian.PutUint16(ip[2:], uint16(ihl+end-off))
		f := uint16(baseOff + off/8)
		if !last || moreFrags {
			f |= 0x2000
		}
		binary.BigEndian.PutUint16(ip[6:], f)
		ip[10], ip[11] = 0, 0
		binary.BigEndian.PutUint16(ip[10:], ipChecksum(ip[:ihl]))
		forward = append(forward, out)
	}
	return forward, nil, true
}

func ipv4Offset(frame []byte) (int, bool) {
	const eth = 14
	if len(frame) < eth {
		return 0, false
	}
	etherType := binary.BigEndian.Uint16(frame[12:14])
	offset := eth
	for tags := 0; etherType == 0x8100 || etherType == 0x88a8 || etherType == 0x9100; tags++ {
		if tags == 2 || len(frame) < offset+4 {
			return 0, false
		}
		etherType = binary.BigEndian.Uint16(frame[offset+2 : offset+4])
		offset += 4
	}
	return offset, etherType == 0x0800
}

// icmpFragNeeded builds the frame that goes back to the device: ICMP type 3 code 4 carrying the
// next-hop MTU and the start of the packet that did not fit, sent as if from the destination.
func icmpFragNeeded(frame []byte, ipOffset, ihl, mtu int) []byte {
	quote := ihl + 8
	if ipOffset+quote > len(frame) {
		quote = len(frame) - ipOffset
	}
	icmpLen := 8 + quote
	out := make([]byte, ipOffset+20+icmpLen)
	copy(out[:ipOffset], frame[:ipOffset])
	copy(out[0:6], frame[6:12]) // to the device
	copy(out[6:12], frame[0:6]) // from the MAC it addressed
	ip := out[ipOffset:]
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:], uint16(20+icmpLen))
	ip[8], ip[9] = 64, 1
	copy(ip[12:16], frame[ipOffset+16:ipOffset+20]) // source: the destination it was sending to
	copy(ip[16:20], frame[ipOffset+12:ipOffset+16]) // destination: the sender
	binary.BigEndian.PutUint16(ip[10:], ipChecksum(ip[:20]))
	ic := ip[20:]
	ic[0], ic[1] = 3, 4
	binary.BigEndian.PutUint16(ic[6:], uint16(mtu))
	copy(ic[8:], frame[ipOffset:ipOffset+quote])
	binary.BigEndian.PutUint16(ic[2:], ipChecksum(ic))
	return out
}

func ipChecksum(b []byte) uint16 {
	var s uint32
	for i := 0; i+1 < len(b); i += 2 {
		s += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		s += uint32(b[len(b)-1]) << 8
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return ^uint16(s)
}

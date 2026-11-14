package lan

import "encoding/binary"

// oversizeIPv4 handles an IPv4 frame larger than the tunnel can carry the way a router does: with
// DF set the device is told the usable MTU (ICMP fragmentation needed), so path-MTU discovery
// shrinks its segments; without DF the packet is split into fragments that fit. It reports the
// frames to send on toward the tunnel and the frames to hand back to the device. ok is false for
// anything that is not a plain IPv4 frame, which stays dropped.
func oversizeIPv4(frame []byte, frameMTU int) (forward, back [][]byte, ok bool) {
	const eth = 14
	if len(frame) < eth+20 || frame[12] != 0x08 || frame[13] != 0x00 || frame[eth]>>4 != 4 {
		return nil, nil, false
	}
	ihl := int(frame[eth]&0x0f) * 4
	total := int(binary.BigEndian.Uint16(frame[eth+2:]))
	if ihl < 20 || total < ihl || eth+total > len(frame) {
		return nil, nil, false
	}
	mtu := frameMTU - eth
	if mtu < ihl+8 {
		return nil, nil, false
	}
	flags := binary.BigEndian.Uint16(frame[eth+6:])
	if flags&0x4000 != 0 { // DF
		return nil, [][]byte{icmpFragNeeded(frame, ihl, mtu)}, true
	}
	hdr := frame[eth : eth+ihl]
	payload := frame[eth+ihl : eth+total]
	baseOff := int(flags & 0x1fff)
	moreFrags := flags&0x2000 != 0
	step := (mtu - ihl) &^ 7
	for off := 0; off < len(payload); off += step {
		end := off + step
		last := end >= len(payload)
		if last {
			end = len(payload)
		}
		out := make([]byte, eth+ihl+end-off)
		copy(out, frame[:eth+ihl])
		copy(out[eth+ihl:], payload[off:end])
		ip := out[eth:]
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
	_ = hdr
	return forward, nil, true
}

// icmpFragNeeded builds the frame that goes back to the device: ICMP type 3 code 4 carrying the
// next-hop MTU and the start of the packet that did not fit, sent as if from the destination.
func icmpFragNeeded(frame []byte, ihl, mtu int) []byte {
	const eth = 14
	quote := ihl + 8
	if eth+quote > len(frame) {
		quote = len(frame) - eth
	}
	icmpLen := 8 + quote
	out := make([]byte, eth+20+icmpLen)
	copy(out[0:6], frame[6:12])  // to the device
	copy(out[6:12], frame[0:6])  // from the MAC it addressed
	out[12], out[13] = 0x08, 0x00
	ip := out[eth:]
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:], uint16(20+icmpLen))
	ip[8], ip[9] = 64, 1
	copy(ip[12:16], frame[eth+16:eth+20]) // source: the destination it was sending to
	copy(ip[16:20], frame[eth+12:eth+16]) // destination: the sender
	binary.BigEndian.PutUint16(ip[10:], ipChecksum(ip[:20]))
	ic := ip[20:]
	ic[0], ic[1] = 3, 4
	binary.BigEndian.PutUint16(ic[6:], uint16(mtu))
	copy(ic[8:], frame[eth:eth+quote])
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

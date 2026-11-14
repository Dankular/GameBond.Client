package lan

import (
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"
)

// l2Trace records what the bridge does with every frame: a counter per direction, class and
// outcome, a size histogram, and the last frames with their verdict. The panel reads it, so a
// failing device can be diagnosed from the screen instead of from packet captures.
type l2Trace struct {
	mu     sync.Mutex
	counts map[string]uint64
	sizes  [8]uint64
	ring   [160]traceEntry
	n      int
}

type traceEntry struct {
	at      time.Time
	verdict string
	desc    string
}

var sizeEdges = [...]int{64, 128, 256, 512, 1024, 1280, 1394}

func (t *l2Trace) note(verdict string, frame []byte) {
	class, desc := describeFrame(frame)
	t.mu.Lock()
	if t.counts == nil {
		t.counts = map[string]uint64{}
	}
	t.counts[verdict+" "+class]++
	i := 0
	for i < len(sizeEdges) && len(frame) > sizeEdges[i] {
		i++
	}
	t.sizes[i]++
	t.ring[t.n%len(t.ring)] = traceEntry{at: time.Now(), verdict: verdict, desc: desc}
	t.n++
	t.mu.Unlock()
}

// snapshot is the panel view: counters by "verdict class", sizes, and the newest frames first.
func (t *l2Trace) snapshot() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	counts := make(map[string]uint64, len(t.counts))
	for k, v := range t.counts {
		counts[k] = v
	}
	sizes := map[string]uint64{}
	labels := []string{"<=64", "<=128", "<=256", "<=512", "<=1024", "<=1280", "<=1394", ">1394"}
	for i, v := range t.sizes {
		sizes[labels[i]] = v
	}
	var recent []string
	for i := 1; i <= len(t.ring) && i <= t.n; i++ {
		e := t.ring[(t.n-i)%len(t.ring)]
		recent = append(recent, fmt.Sprintf("%s %-12s %s", e.at.Format("15:04:05.000"), e.verdict, e.desc))
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return map[string]any{"frames": counts, "frame_sizes": sizes, "recent": recent}
}

// describeFrame returns a short class (arp, ipv4-tcp, ipv4-udp-dhcp, ...) and a one-line summary.
func describeFrame(f []byte) (class, desc string) {
	if len(f) < 14 {
		return "runt", fmt.Sprintf("%d bytes", len(f))
	}
	head := fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x>%02x:%02x:%02x:%02x:%02x:%02x len=%d ",
		f[6], f[7], f[8], f[9], f[10], f[11], f[0], f[1], f[2], f[3], f[4], f[5], len(f))
	switch et := binary.BigEndian.Uint16(f[12:]); et {
	case 0x0806:
		if len(f) >= 42 {
			op := "request"
			if f[21] == 2 {
				op = "reply"
			}
			return "arp-" + op, head + fmt.Sprintf("ARP %s %d.%d.%d.%d is asked by %d.%d.%d.%d", op, f[38], f[39], f[40], f[41], f[28], f[29], f[30], f[31])
		}
		return "arp", head + "ARP short"
	case 0x0800:
		if len(f) < 34 {
			return "ipv4", head + "IPv4 short"
		}
		ip := f[14:]
		ihl := int(ip[0]&0x0f) * 4
		src, dst := fmt.Sprintf("%d.%d.%d.%d", ip[12], ip[13], ip[14], ip[15]), fmt.Sprintf("%d.%d.%d.%d", ip[16], ip[17], ip[18], ip[19])
		fl := binary.BigEndian.Uint16(ip[6:])
		extra := ""
		if fl&0x4000 != 0 {
			extra += " DF"
		}
		if fl&0x3fff != 0 {
			extra += fmt.Sprintf(" frag@%d", (fl&0x1fff)*8)
		}
		switch ip[9] {
		case 6, 17:
			if len(f) >= 14+ihl+4 {
				sp, dp := binary.BigEndian.Uint16(ip[ihl:]), binary.BigEndian.Uint16(ip[ihl+2:])
				name := "tcp"
				flags := ""
				if ip[9] == 17 {
					name = "udp"
					if sp == 67 || sp == 68 || dp == 67 || dp == 68 {
						name = "udp-dhcp"
					}
				} else if len(f) >= 14+ihl+14 {
					b := ip[ihl+13]
					for i, n := range []string{"F", "S", "R", "P", "A", "U"} {
						if b&(1<<i) != 0 {
							flags += n
						}
					}
					flags = " [" + flags + "]" + tcpDetail(ip, ihl)
				}
				if ip[9] == 17 && (sp == 53 || dp == 53) && len(f) >= 14+ihl+8+12 {
					name = "udp-dns"
					flags = " " + describeDNS(ip[ihl+8:min(len(ip), int(binary.BigEndian.Uint16(ip[2:])))], sp == 53)
				}
				return "ipv4-" + name, head + fmt.Sprintf("%s %s:%d>%s:%d%s%s", name, src, sp, dst, dp, flags, extra)
			}
		case 1:
			return "ipv4-icmp", head + fmt.Sprintf("ICMP %s>%s type=%d code=%d%s", src, dst, ip[ihl], ip[ihl+1], extra)
		case 2:
			return "ipv4-igmp", head + "IGMP " + src + ">" + dst
		}
		return fmt.Sprintf("ipv4-proto%d", ip[9]), head + fmt.Sprintf("IPv4 proto %d %s>%s%s", ip[9], src, dst, extra)
	case 0x86dd:
		if len(f) < 14+40 {
			return "ipv6", head + "IPv6 short"
		}
		ip := f[14:]
		src, dst := net.IP(ip[8:24]).String(), net.IP(ip[24:40]).String()
		switch nh := ip[6]; nh {
		case 58:
			if len(ip) > 40 {
				return fmt.Sprintf("ipv6-icmp%d", ip[40]), head + fmt.Sprintf("ICMPv6 type=%d %s>%s", ip[40], src, dst)
			}
		case 17:
			if len(ip) >= 48 {
				sp, dp := binary.BigEndian.Uint16(ip[40:]), binary.BigEndian.Uint16(ip[42:])
				if sp == 53 || dp == 53 {
					return "ipv6-udp-dns", head + fmt.Sprintf("UDP6 [%s]:%d>[%s]:%d %s", src, sp, dst, dp, describeDNS(ip[48:], sp == 53))
				}
				return "ipv6-udp", head + fmt.Sprintf("UDP6 [%s]:%d>[%s]:%d", src, sp, dst, dp)
			}
		case 6:
			if len(ip) >= 44 {
				return "ipv6-tcp", head + fmt.Sprintf("TCP6 [%s]:%d>[%s]:%d", src, binary.BigEndian.Uint16(ip[40:]), dst, binary.BigEndian.Uint16(ip[42:]))
			}
		}
		return fmt.Sprintf("ipv6-nh%d", ip[6]), head + fmt.Sprintf("IPv6 nh=%d %s>%s", ip[6], src, dst)
	default:
		return fmt.Sprintf("ethertype-%04x", et), head + fmt.Sprintf("ethertype %04x", et)
	}
}

// describeDNS summarises a DNS message: the question, and for a response the result code and answers.
func describeDNS(m []byte, response bool) string {
	if len(m) < 12 {
		return "dns short"
	}
	flags := binary.BigEndian.Uint16(m[2:])
	an := binary.BigEndian.Uint16(m[6:])
	name, off := "", 12
	for off < len(m) {
		l := int(m[off])
		if l == 0 {
			off++
			break
		}
		if l&0xc0 != 0 || off+1+l > len(m) {
			return "dns?"
		}
		if name != "" {
			name += "."
		}
		name += string(m[off+1 : off+1+l])
		off += 1 + l
	}
	qt := uint16(0)
	if off+2 <= len(m) {
		qt = binary.BigEndian.Uint16(m[off:])
	}
	types := map[uint16]string{1: "A", 5: "CNAME", 28: "AAAA", 65: "HTTPS", 15: "MX", 16: "TXT", 12: "PTR", 33: "SRV"}
	t := types[qt]
	if t == "" {
		t = fmt.Sprintf("type%d", qt)
	}
	if !response {
		return fmt.Sprintf("Q %s %s", t, name)
	}
	rc := []string{"NOERROR", "FORMERR", "SERVFAIL", "NXDOMAIN", "NOTIMP", "REFUSED"}
	r := int(flags & 0xf)
	rs := fmt.Sprintf("rcode%d", r)
	if r < len(rc) {
		rs = rc[r]
	}
	tc := ""
	if flags&0x0200 != 0 {
		tc = " TRUNCATED"
	}
	return fmt.Sprintf("A %s %s %s answers=%d%s", t, name, rs, an, tc)
}

// tcpDetail adds sequence, acknowledgement, window, payload length and SACK blocks to a TCP summary, the
// numbers needed to see which segment a peer says it is missing.
func tcpDetail(ip []byte, ihl int) string {
	t := ip[ihl:]
	if len(t) < 20 {
		return ""
	}
	off := int(t[12]>>4) * 4
	total := int(binary.BigEndian.Uint16(ip[2:]))
	out := fmt.Sprintf(" seq=%d ack=%d win=%d len=%d", binary.BigEndian.Uint32(t[4:]), binary.BigEndian.Uint32(t[8:]), binary.BigEndian.Uint16(t[14:]), total-ihl-off)
	for i := 20; i+2 <= off && i+2 <= len(t); {
		k := t[i]
		if k == 0 {
			break
		}
		if k == 1 {
			i++
			continue
		}
		l := int(t[i+1])
		if l < 2 || i+l > len(t) {
			break
		}
		switch k {
		case 2:
			if l == 4 {
				out += fmt.Sprintf(" mss=%d", binary.BigEndian.Uint16(t[i+2:]))
			}
		case 5:
			for j := i + 2; j+8 <= i+l; j += 8 {
				out += fmt.Sprintf(" sack=%d-%d", binary.BigEndian.Uint32(t[j:]), binary.BigEndian.Uint32(t[j+4:]))
			}
		}
		i += l
	}
	return out
}

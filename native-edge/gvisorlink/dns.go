package gvisorlink

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// The client's gVisor stack is the DNS server of the bridged LAN: the DHCP lease names the client's
// segment address as the resolver, the stack receives queries on the virtual LAN NIC, and the
// answers come from the configured upstream resolvers. Nothing here depends on the exit.

var defaultUpstreamDNS = []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("8.8.8.8")}

const dnsTimeout = 2 * time.Second

// dnsPort is the upstream resolvers' port; tests point it at a local fake.
var dnsPort = "53"

// startDNS binds UDP port 53 on the link's stack. Queries arrive only through HandleLANFrame, which
// injects them into the virtual LAN NIC.
func (l *Link) startDNS(ctx context.Context) error {
	queue := new(waiter.Queue)
	ep, err := l.stack.NewEndpoint(udp.ProtocolNumber, ipv4.ProtocolNumber, queue)
	if err != nil {
		return fmt.Errorf("gvisorlink: create DNS endpoint: %s", err)
	}
	if err := ep.Bind(tcpip.FullAddress{Port: 53}); err != nil {
		ep.Close()
		return fmt.Errorf("gvisorlink: bind DNS port: %s", err)
	}
	l.dnsEP = ep
	entry, ready := waiter.NewChannelEntry(waiter.ReadableEvents)
	queue.EventRegister(&entry)
	l.done.Add(1)
	go func() {
		defer l.done.Done()
		defer queue.EventUnregister(&entry)
		l.serveDNS(ctx, ep, ready)
	}()
	return nil
}

func (l *Link) serveDNS(ctx context.Context, ep tcpip.Endpoint, ready <-chan struct{}) {
	for ctx.Err() == nil {
		var message bytes.Buffer
		result, err := ep.Read(&message, tcpip.ReadOptions{NeedRemoteAddr: true})
		if err != nil {
			if _, blocked := err.(*tcpip.ErrWouldBlock); !blocked {
				return
			}
			select {
			case <-ready:
			case <-ctx.Done():
				return
			}
			continue
		}
		query := append([]byte(nil), message.Bytes()...)
		if len(query) < 12 {
			continue
		}
		to := &tcpip.FullAddress{NIC: lanNIC, Addr: result.RemoteAddr.Addr, Port: result.RemoteAddr.Port}
		l.mu.Lock()
		upstream := append([]netip.Addr(nil), l.dnsUpstream...)
		l.mu.Unlock()
		go func() {
			if reply := forwardDNS(ctx, query, upstream); reply != nil {
				_, _ = ep.Write(bytes.NewReader(reply), tcpip.WriteOptions{To: to})
			}
		}()
	}
}

// forwardDNS asks the upstream resolvers in turn and returns the first answer, or a SERVFAIL for the
// query when none answers, so a device fails fast instead of waiting out its own timeout.
func forwardDNS(ctx context.Context, query []byte, upstream []netip.Addr) []byte {
	if len(upstream) == 0 {
		upstream = defaultUpstreamDNS
	}
	for _, server := range upstream {
		if ctx.Err() != nil {
			return nil
		}
		conn, err := net.DialTimeout("udp", net.JoinHostPort(server.String(), dnsPort), dnsTimeout)
		if err != nil {
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(dnsTimeout))
		reply := make([]byte, 4096)
		if _, err = conn.Write(query); err == nil {
			var n int
			if n, err = conn.Read(reply); err == nil && n >= 12 && reply[0] == query[0] && reply[1] == query[1] {
				conn.Close()
				return reply[:n]
			}
		}
		conn.Close()
	}
	fail := append([]byte(nil), query[:12]...)
	fail[2], fail[3] = 0x81|query[2]&0x01, 0x82 // response, recursion available, SERVFAIL
	return fail
}

// dnsFrame reports whether frame is a DNS query, over IPv4 UDP, addressed to the client's own
// address on the virtual LAN NIC.
func dnsFrame(frame []byte, client netip.Addr) bool {
	if len(frame) < 14+20+8+12 || binary.BigEndian.Uint16(frame[12:14]) != 0x0800 || !bytes.Equal(frame[0:6], lanMACBytes) {
		return false
	}
	ip := frame[14:]
	headerLen := int(ip[0]&0x0f) * 4
	if ip[0]>>4 != 4 || headerLen < 20 || ip[9] != 17 || len(ip) < headerLen+8+12 {
		return false
	}
	return binary.BigEndian.Uint16(ip[headerLen+2:]) == 53 && bytes.Equal(ip[16:20], client.AsSlice())
}

// arpForClient reports whether frame is ARP the virtual LAN NIC must see: a request for the client's
// address (the device looking for its DNS server), or any reply addressed to the NIC (the device
// answering the NIC's own request for its MAC, without which a reply to it can never be sent once
// the neighbour entry expires).
func arpForClient(frame []byte, client netip.Addr) bool {
	if len(frame) < 42 || binary.BigEndian.Uint16(frame[12:14]) != 0x0806 {
		return false
	}
	switch binary.BigEndian.Uint16(frame[20:22]) {
	case 1:
		return bytes.Equal(frame[38:42], client.AsSlice())
	case 2:
		return bytes.Equal(frame[0:6], lanMACBytes)
	}
	return false
}

func (l *Link) injectLAN(frame []byte) {
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(append([]byte(nil), frame...))})
	l.lan.InjectInbound(0, pkt)
	pkt.DecRef()
}

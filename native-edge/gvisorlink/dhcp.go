package gvisorlink

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"gamebond.local/nativeedge/lan"
	"github.com/Dankular/GameBond.Core/bond"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/link/ethernet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// DHCPConfig is the client-side DHCPv4 service for the devices bridged onto the bond network
// from the client's adapter (an attached access point and the PS5 behind it). The client's gVisor
// stack answers it, using the subnet and gateway the exit provisions. Only fixed reservations are
// served for now; a pool can be added later without changing the link or the bridge hook.
//
// The exit normally supplies the hosts and DNS in its provisioning (client_hosts, client_dns), so
// an empty DHCPConfig is valid: it serves only what the exit delegates. Reservations here are a
// local override that applies when the exit delegates nothing.
type DHCPConfig struct {
	Reservations []lan.Reservation
	DNS          []netip.Addr
	LeaseSeconds int
}

// Validate checks the settings that do not depend on the provisioned subnet.
func (c DHCPConfig) Validate() error {
	seenMAC, seenIP := map[[6]byte]bool{}, map[netip.Addr]bool{}
	for _, r := range c.Reservations {
		if r.MAC == ([6]byte{}) || r.MAC[0]&1 != 0 || !r.IP.Is4() || r.IP.IsUnspecified() || r.IP.IsMulticast() {
			return fmt.Errorf("gvisorlink: invalid DHCP reservation %q", r.Name)
		}
		if seenMAC[r.MAC] || seenIP[r.IP] {
			return fmt.Errorf("gvisorlink: duplicate DHCP reservation %q", r.Name)
		}
		seenMAC[r.MAC], seenIP[r.IP] = true, true
	}
	for _, d := range c.DNS {
		if !d.Is4() {
			return errors.New("gvisorlink: DHCP DNS servers must be IPv4")
		}
	}
	if c.LeaseSeconds < 0 || c.LeaseSeconds > 7*24*3600 {
		return errors.New("gvisorlink: DHCP lease_seconds out of range")
	}
	return nil
}

// buildDHCP creates the server for the current provisioning. The exit address is the router
// option; the client's own bond address is the server identifier, because that is the address the
// virtual LAN NIC owns.
// The lease names the client itself as the DNS server (the link answers it, see dns.go); the
// configured or provisioned servers become the upstream resolvers it forwards to. The hosts are the
// reserved device addresses, which need a route out of the virtual LAN NIC.
func buildDHCP(c *DHCPConfig, p *bond.GatewayProvisioning, client netip.Prefix, exit netip.Addr) (server *lan.DHCPServer, upstream, hosts []netip.Addr, err error) {
	if c == nil {
		return nil, nil, nil, nil
	}
	reservations, dns := c.Reservations, c.DNS
	if len(p.ClientHosts) > 0 { // the exit is the source of truth
		reservations = make([]lan.Reservation, 0, len(p.ClientHosts))
		for _, h := range p.ClientHosts {
			mac, err := net.ParseMAC(h.MAC)
			ip, ipErr := netip.ParseAddr(h.IP)
			if err != nil || len(mac) != 6 || ipErr != nil {
				return nil, nil, nil, fmt.Errorf("gvisorlink: provisioned DHCP host %q is malformed", h.Name)
			}
			reservations = append(reservations, lan.Reservation{MAC: [6]byte(mac), IP: ip, Name: h.Name})
		}
		if len(dns) == 0 {
			for _, d := range p.ClientDNS {
				a, err := netip.ParseAddr(d)
				if err != nil {
					return nil, nil, nil, fmt.Errorf("gvisorlink: provisioned DNS server %q is malformed", d)
				}
				dns = append(dns, a)
			}
		}
	}
	if len(reservations) == 0 {
		return nil, nil, nil, nil // nothing delegated: the exit's own DHCP keeps answering
	}
	subnet := client.Masked()
	var start, end netip.Addr
	for _, r := range reservations {
		if !subnet.Contains(r.IP) || r.IP == exit || r.IP == client.Addr() || r.IP == subnet.Addr() {
			return nil, nil, nil, fmt.Errorf("gvisorlink: DHCP reservation %s is not a usable host address in %s", r.IP, subnet)
		}
		if !start.IsValid() || r.IP.Less(start) {
			start = r.IP
		}
		if !end.IsValid() || end.Less(r.IP) {
			end = r.IP
		}
	}
	lease := c.LeaseSeconds
	if lease == 0 {
		lease = 3600
	}
	network := lan.NetworkConfig{Name: "bond-lan", Prefix: netip.PrefixFrom(exit, client.Bits()), ServerID: client.Addr(), MTU: int(p.Link.MTU),
		DHCP: &lan.DHCPPoolConfig{Start: start, End: end, LeaseSeconds: lease, DNS: []netip.Addr{client.Addr()}, Reservations: reservations}}
	server, err = lan.NewDHCPServer(network, [6]byte(lanMACBytes), nil, time.Now)
	for _, r := range reservations {
		hosts = append(hosts, r.IP)
	}
	return server, dns, hosts, err
}

// startDHCP creates the virtual LAN NIC and the UDP port 67 endpoint on the link's gVisor stack.
// No OS interface is involved: the NIC is a channel endpoint like the app and bond NICs. It only
// receives DHCP frames (HandleLANFrame injects nothing else), so it never answers ARP for the bond
// address on the bridged adapter. Replies leave through the NIC as ordinary broadcasts built by
// the stack and are handed to write.
func (l *Link) startDHCP(ctx context.Context, write func([]byte)) error {
	if write == nil {
		return errInvalidConfig
	}
	l.lanWrite = write
	l.lan = channel.New(256, 1500, lanMAC)
	if err := l.stack.CreateNIC(lanNIC, ethernet.New(l.lan)); err != nil {
		return fmt.Errorf("gvisorlink: create LAN NIC: %s", err)
	}
	queue := new(waiter.Queue)
	ep, err := l.stack.NewEndpoint(udp.ProtocolNumber, ipv4.ProtocolNumber, queue)
	if err != nil {
		return fmt.Errorf("gvisorlink: create DHCP endpoint: %s", err)
	}
	ep.SocketOptions().SetBroadcast(true)
	if err := ep.Bind(tcpip.FullAddress{Port: 67}); err != nil {
		ep.Close()
		return fmt.Errorf("gvisorlink: bind DHCP port: %s", err)
	}
	l.dhcpEP = ep
	entry, ready := waiter.NewChannelEntry(waiter.ReadableEvents)
	queue.EventRegister(&entry)
	l.done.Add(2)
	go func() {
		defer l.done.Done()
		defer queue.EventUnregister(&entry)
		l.serveDHCP(ctx, ep, ready)
	}()
	go l.pumpLAN(ctx)
	return l.startDNS(ctx)
}

func (l *Link) addLANAddress(address tcpip.ProtocolAddress) error {
	if l.lan == nil {
		return nil
	}
	if err := l.stack.AddProtocolAddress(lanNIC, address, stack.AddressProperties{}); err != nil {
		return fmt.Errorf("gvisorlink: configure LAN address: %s", err)
	}
	l.lanAddr = address.AddressWithPrefix.Address
	return nil
}

func (l *Link) removeLANAddress() {
	if l.lan != nil && l.lanAddr.Len() != 0 {
		_ = l.stack.RemoveAddress(lanNIC, l.lanAddr)
	}
	l.lanAddr = tcpip.Address{}
}

func (l *Link) serveDHCP(ctx context.Context, ep tcpip.Endpoint, ready <-chan struct{}) {
	to := &tcpip.FullAddress{NIC: lanNIC, Addr: tcpip.AddrFrom4([4]byte{255, 255, 255, 255}), Port: 68}
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
		l.mu.Lock()
		server := l.dhcp
		l.mu.Unlock()
		request := message.Bytes()
		if server == nil || len(request) < 240 {
			continue
		}
		var chaddr [6]byte
		copy(chaddr[:], request[28:34]) // HandleLANFrame already proved it equals the Ethernet source
		source := netip.AddrFrom4(result.RemoteAddr.Addr.As4())
		if reply, _ := server.HandleMessage(chaddr, source, request); reply != nil {
			_, _ = ep.Write(bytes.NewReader(reply), tcpip.WriteOptions{To: to})
		}
	}
}

func (l *Link) pumpLAN(ctx context.Context) {
	defer l.done.Done()
	for {
		pkt := l.lan.ReadContext(ctx)
		if pkt == nil {
			return
		}
		view := pkt.ToView()
		frame := append([]byte(nil), view.AsSlice()...)
		view.Release()
		pkt.DecRef()
		l.lanWrite(frame)
	}
}

// HandleLANFrame is the bridge's local handler. A DHCP client frame from a bridged device is
// injected into the virtual LAN NIC and consumed; the reply arrives later through LANWrite. Every
// other frame, and every frame while the link is inactive or has no DHCP, is left to the bridge,
// so the exit's own DHCP still answers when the link is not serving.
func (l *Link) HandleLANFrame(frame []byte) ([]byte, bool) {
	l.mu.Lock()
	serving := l.dhcp != nil && l.lan != nil
	client := l.clientAddr.Addr()
	l.mu.Unlock()
	if !serving {
		return nil, false
	}
	if client.IsValid() && (dnsFrame(frame, client) || arpForClient(frame, client)) {
		l.injectLAN(frame)
		return nil, true
	}
	if len(frame) < 14+20+8+240 || binary.BigEndian.Uint16(frame[12:14]) != 0x0800 {
		return nil, false
	}
	ip := frame[14:]
	headerLen := int(ip[0]&0x0f) * 4
	if ip[0]>>4 != 4 || headerLen < 20 || ip[9] != 17 || len(ip) < headerLen+8+240 {
		return nil, false
	}
	udpHeader := ip[headerLen:]
	if binary.BigEndian.Uint16(udpHeader[0:2]) != 68 || binary.BigEndian.Uint16(udpHeader[2:4]) != 67 {
		return nil, false
	}
	if !bytes.Equal(udpHeader[8+28:8+34], frame[6:12]) {
		return nil, true // chaddr must equal the Ethernet source: drop a spoofed request
	}
	l.injectLAN(frame)
	return nil, true
}

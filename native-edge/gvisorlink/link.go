// Package gvisorlink joins the client's IP packet stream and Ethernet bridge
// to the exit's gVisor gateway over the bonded Layer 2 transport.
package gvisorlink

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync"
	"time"

	"gamebond.local/nativeedge/lan"
	"github.com/Dankular/GameBond.Core/bond"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/link/ethernet"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

const (
	appNIC             tcpip.NICID = 1
	bondNIC            tcpip.NICID = 2
	lanNIC             tcpip.NICID = 3 // virtual NIC for the bridged LAN's DHCP; never answers ARP
	ethernetHeaderSize             = 14
	maxFlowMetadata                = 8192
)

var (
	errInvalidConfig = errors.New("gvisorlink: invalid link configuration")
	appMACBytes      = []byte{0x02, 0x47, 0x42, 0, 0, 2}
	bondMACBytes     = []byte{0x02, 0x47, 0x42, 0, 0, 4}
	appMAC           = tcpip.LinkAddress(string(appMACBytes))
	bondMAC          = tcpip.LinkAddress(string(bondMACBytes))
	peerMAC          = []byte{0x02, 0x47, 0x42, 0, 0, 3}
	lanMACBytes      = []byte{0x02, 0x47, 0x42, 0, 0, 5}
	lanMAC           = tcpip.LinkAddress(string(lanMACBytes))
)

// Link carries packets between the native client netstack and the exit's
// gVisor gateway. Ethernet bridge frames share the same bonded transport.
type Link struct {
	mu         sync.Mutex
	stack      *stack.Stack
	app        *channel.Endpoint
	bond       *channel.Endpoint
	cancel     context.CancelFunc
	done       sync.WaitGroup
	send       func(bond.QueuedPacket) bool
	deliverIP  func([]byte)
	clientIP   netip.Addr
	active     bool
	clientAddr netip.Prefix
	exitAddr   netip.Addr
	mtu        uint16
	routes     []string
	flows      map[string]flowMetadata
	dhcpCfg    *DHCPConfig
	dhcp       *lan.DHCPServer
	lan        *channel.Endpoint
	dhcpEP     tcpip.Endpoint
	delegation string // the provisioned client hosts and DNS the current DHCP server was built from
	lanWrite   func([]byte)
	lanAddr    tcpip.Address
}

type flowMetadata struct {
	packet  bond.QueuedPacket
	expires time.Time
}

// Options provide the client host address and the bonded packet callbacks.
type Options struct {
	ClientIP  netip.Addr
	Send      func(bond.QueuedPacket) bool
	DeliverIP func([]byte)
	// DHCP, when set, answers DHCP for the bridged LAN device from the provisioned subnet.
	DHCP *DHCPConfig
	// LANWrite sends a frame the virtual LAN NIC produced (a DHCP reply) to the bridged adapter.
	LANWrite func([]byte)
}

// New creates a client-side gVisor router. It remains inactive until an
// authenticated GatewayProvisioning arrives from the active exit.
func New(options Options) (*Link, error) {
	if !options.ClientIP.Is4() || options.Send == nil || options.DeliverIP == nil {
		return nil, errInvalidConfig
	}
	app := channel.New(1024, 9000, appMAC)
	bondEP := channel.New(4096, 9000, bondMAC)
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, arp.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{udp.NewProtocol, tcp.NewProtocol, icmp.NewProtocol4},
	})
	if err := s.CreateNIC(appNIC, ethernet.New(app)); err != nil {
		s.Close()
		return nil, fmt.Errorf("gvisorlink: create app NIC: %s", err)
	}
	if err := s.CreateNIC(bondNIC, ethernet.New(bondEP)); err != nil {
		s.Close()
		return nil, fmt.Errorf("gvisorlink: create bond NIC: %s", err)
	}
	if err := installMasquerade(s, options.ClientIP); err != nil {
		s.Close()
		return nil, err
	}
	if err := s.SetSpoofing(appNIC, true); err != nil {
		s.Close()
		return nil, fmt.Errorf("gvisorlink: enable app NIC spoofing: %s", err)
	}
	if err := s.SetForwardingDefaultAndAllNICs(ipv4.ProtocolNumber, true); err != nil {
		s.Close()
		return nil, fmt.Errorf("gvisorlink: enable IPv4 forwarding: %s", err)
	}
	if err := s.AddStaticNeighbor(appNIC, ipv4.ProtocolNumber, tcpip.AddrFrom4(options.ClientIP.As4()), appMAC); err != nil {
		s.Close()
		return nil, fmt.Errorf("gvisorlink: configure app neighbor: %s", err)
	}
	if options.DHCP != nil {
		if err := options.DHCP.Validate(); err != nil {
			s.Close()
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &Link{dhcpCfg: options.DHCP, stack: s, app: app, bond: bondEP, cancel: cancel, send: options.Send, deliverIP: options.DeliverIP,
		clientIP: options.ClientIP, flows: make(map[string]flowMetadata)}
	l.done.Add(2)
	go l.pumpBond(ctx)
	go l.pumpClient(ctx)
	if options.DHCP != nil {
		if err := l.startDHCP(ctx, options.LANWrite); err != nil {
			cancel()
			l.done.Wait()
			s.Close()
			return nil, err
		}
	}
	return l, nil
}

// installMasquerade makes the client stack the NAT for its own applications. Their packets carry the
// local tunnel address, which is not part of the provisioned segment, so on the way out of the stack
// the source becomes the client's bond address (the address of the NIC the packet leaves through).
// gVisor's connection tracking reverses the translation for the replies, so the exit only ever sees
// the segment address and needs no knowledge of the application side.
func installMasquerade(s *stack.Stack, client netip.Addr) error {
	accept := func() stack.Rule {
		return stack.Rule{Filter: stack.EmptyFilter4(), Target: &stack.AcceptTarget{NetworkProtocol: ipv4.ProtocolNumber}}
	}
	// One rule per protocol the stack parses. The target reads the transport header, which gVisor only
	// fills in for registered protocols, so anything else must never reach it.
	var masquerade []stack.Rule
	for _, proto := range []tcpip.TransportProtocolNumber{header.TCPProtocolNumber, header.UDPProtocolNumber, header.ICMPv4ProtocolNumber} {
		r := stack.Rule{Filter: stack.EmptyFilter4(), Target: &stack.MasqueradeTarget{NetworkProtocol: ipv4.ProtocolNumber}}
		r.Filter.Src = tcpip.AddrFrom4(client.As4())
		r.Filter.SrcMask = tcpip.AddrFrom4([4]byte{255, 255, 255, 255})
		r.Filter.Protocol, r.Filter.CheckProtocol = proto, true
		masquerade = append(masquerade, r)
	}
	table := stack.EmptyNATTable()
	table.Rules = append([]stack.Rule{accept(), accept(), accept()}, masquerade...)
	table.Rules = append(table.Rules, accept(), stack.Rule{Filter: stack.EmptyFilter4(), Target: &stack.ErrorTarget{NetworkProtocol: ipv4.ProtocolNumber}})
	table.BuiltinChains[stack.Prerouting], table.Underflows[stack.Prerouting] = 0, 0
	table.BuiltinChains[stack.Input], table.Underflows[stack.Input] = 1, 1
	table.BuiltinChains[stack.Output], table.Underflows[stack.Output] = 2, 2
	table.BuiltinChains[stack.Postrouting], table.Underflows[stack.Postrouting] = 3, 3+len(masquerade)
	s.IPTables().ForceReplaceTable(stack.NATID, table, false)
	return nil
}

// Configure applies an authenticated topology assignment. A disabled exit
// closes the virtual route; primary and passthrough both use the assigned
// shared gateway address.
func (l *Link) Configure(p *bond.GatewayProvisioning) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if p == nil || p.Mode == bond.GatewayDisabled {
		if l.active {
			if l.clientAddr.IsValid() {
				_ = l.stack.RemoveAddress(bondNIC, tcpip.AddrFrom4(l.clientAddr.Addr().As4()))
			}
			l.stack.SetRouteTable(nil)
		}
		l.removeLANAddress()
		l.active = false
		l.dhcp = nil
		l.clientAddr, l.exitAddr, l.mtu = netip.Prefix{}, netip.Addr{}, 0
		l.routes = nil
		l.bond.SetMTU(9000)
		return nil
	}
	if err := p.Validate(); err != nil {
		return fmt.Errorf("gvisorlink: configure bond address: %s", err)
	}
	client, _ := netip.ParsePrefix(p.Link.ClientAddress)
	exit, _ := netip.ParseAddr(p.Link.ExitAddress)
	if client.Addr() == l.clientIP || l.clientIP == exit {
		return errInvalidConfig
	}
	delegation := fmt.Sprint(p.ClientHosts, p.ClientDNS)
	if l.active && l.clientAddr == client && l.exitAddr == exit && l.mtu == p.Link.MTU && slices.Equal(l.routes, p.Routes) && l.delegation == delegation {
		return nil
	}
	dhcpServer, err := buildDHCP(l.dhcpCfg, p, client, exit)
	if err != nil {
		return err
	}
	if l.active && l.clientAddr.IsValid() {
		_ = l.stack.RemoveAddress(bondNIC, tcpip.AddrFrom4(l.clientAddr.Addr().As4()))
	}
	l.removeLANAddress()
	protocolAddress := tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: tcpip.AddressWithPrefix{
		Address: tcpip.AddrFrom4(client.Addr().As4()), PrefixLen: client.Bits(),
	}}
	if err := l.stack.AddProtocolAddress(bondNIC, protocolAddress, stack.AddressProperties{}); err != nil {
		return fmt.Errorf("gvisorlink: configure client address: %s", err)
	}
	appMask := tcpip.MaskFromBytes([]byte{255, 255, 255, 255})
	appSubnet, err := tcpip.NewSubnet(tcpip.AddrFrom4(l.clientIP.As4()), appMask)
	if err != nil {
		_ = l.stack.RemoveAddress(bondNIC, protocolAddress.AddressWithPrefix.Address)
		return fmt.Errorf("gvisorlink: configure app route: %s", err)
	}
	routeTable := []tcpip.Route{
		{Destination: appSubnet, NIC: appNIC},
	}
	for _, route := range p.Routes {
		prefix, _ := netip.ParsePrefix(route) // Validate above guarantees a canonical IPv4 prefix.
		subnet, subnetErr := tcpip.NewSubnet(tcpip.AddrFrom4(prefix.Addr().As4()), tcpip.MaskFromBytes(netipPrefixMask(prefix)))
		if subnetErr != nil {
			_ = l.stack.RemoveAddress(bondNIC, protocolAddress.AddressWithPrefix.Address)
			return fmt.Errorf("gvisorlink: configure provisioned route %s: %s", route, subnetErr)
		}
		routeTable = append(routeTable, tcpip.Route{Destination: subnet, Gateway: tcpip.AddrFrom4(exit.As4()), NIC: bondNIC})
	}
	routeTable = append(routeTable, tcpip.Route{Destination: header.IPv4EmptySubnet, Gateway: tcpip.AddrFrom4(exit.As4()), NIC: bondNIC})
	if dhcpServer != nil {
		if err := l.addLANAddress(protocolAddress); err != nil {
			_ = l.stack.RemoveAddress(bondNIC, protocolAddress.AddressWithPrefix.Address)
			return err
		}
	}
	l.stack.SetRouteTable(routeTable)
	l.bond.SetMTU(uint32(p.Link.MTU))
	l.clientAddr, l.exitAddr, l.mtu, l.active = client, exit, p.Link.MTU, true
	l.dhcp, l.delegation = dhcpServer, delegation
	l.routes = append(l.routes[:0], p.Routes...)
	return nil
}

func netipPrefixMask(prefix netip.Prefix) []byte {
	mask := make([]byte, 4)
	bits := prefix.Bits()
	for i := range mask {
		remaining := bits - i*8
		if remaining >= 8 {
			mask[i] = 0xff
		} else if remaining > 0 {
			mask[i] = byte(0xff << (8 - remaining))
		}
	}
	return mask
}

// Active reports whether an authenticated provisioning currently enables the link.
func (l *Link) Active() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active
}

// Status reports the active client and exit addresses for the runtime status file.
func (l *Link) Status() map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	status := map[string]any{"active": l.active}
	if l.active {
		status["client_address"] = l.clientAddr.String()
		status["exit_address"] = l.exitAddr.String()
		status["mtu"] = l.mtu
		status["routes"] = append([]string(nil), l.routes...)
	}
	return status
}

// ForwardIP injects an IP packet from the client netstack into the gVisor
// router. It returns false when this link is inactive or the packet is not IPv4.
func (l *Link) ForwardIP(q bond.QueuedPacket) bool {
	if len(q.Payload) == 0 || q.Payload[0]>>4 != 4 {
		return false
	}
	info, err := bond.ParseIPv4(q.Payload)
	if err != nil {
		return false
	}
	l.mu.Lock()
	if !l.active {
		l.mu.Unlock()
		return false
	}
	payload := append([]byte(nil), q.Payload...)
	q.Payload = nil
	if q.Flow == "" {
		q.Flow = info.Flow
	}
	l.flows[info.Flow] = flowMetadata{packet: q, expires: time.Now().Add(30 * time.Second)}
	if len(l.flows) > maxFlowMetadata {
		l.pruneFlows(time.Now())
	}
	frame := make([]byte, ethernetHeaderSize+len(payload))
	copy(frame[:6], appMACBytes)
	copy(frame[6:12], peerMAC)
	binary.BigEndian.PutUint16(frame[12:14], 0x0800)
	copy(frame[ethernetHeaderSize:], payload)
	l.mu.Unlock()
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(frame)})
	l.app.InjectInbound(0, pkt)
	pkt.DecRef()
	return true
}

// SendEthernet sends one LAN bridge frame over the same gVisor/bond link.
func (l *Link) SendEthernet(q bond.QueuedPacket) bool {
	if len(q.Payload) == 0 || q.Payload[0] != bond.L2Marker || l.send == nil {
		return false
	}
	return l.send(q)
}

// ReceiveEthernet feeds a frame from the exit into the client gVisor link. It
// returns true when the frame belongs to the client stack and should not be
// delivered to a physical LAN adapter.
func (l *Link) ReceiveEthernet(frame []byte) bool {
	if len(frame) < ethernetHeaderSize || len(frame) > 9216 {
		return false
	}
	if l.bond == nil {
		return false
	}
	l.mu.Lock()
	active := l.active
	l.mu.Unlock()
	if !active {
		return false
	}
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(append([]byte(nil), frame...))})
	l.bond.InjectInbound(0, pkt)
	pkt.DecRef()
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.active {
		return false
	}
	if string(frame[:6]) == string(bondMACBytes) {
		return true
	}
	return ethernetIPv4Destination(frame) == l.clientIP
}

// Close stops the packet pumps and closes the gVisor stack.
func (l *Link) Close() {
	if l == nil {
		return
	}
	l.cancel()
	l.stack.Close()
	l.done.Wait()
}

func (l *Link) pumpBond(ctx context.Context) {
	defer l.done.Done()
	for {
		pkt := l.bond.ReadContext(ctx)
		if pkt == nil {
			return
		}
		view := pkt.ToView()
		frame := append([]byte(nil), view.AsSlice()...)
		view.Release()
		pkt.DecRef()
		if len(frame) < ethernetHeaderSize {
			continue
		}
		l.mu.Lock()
		active := l.active
		l.mu.Unlock()
		if !active {
			continue
		}
		q := l.packetForFrame(frame)
		q.Payload = bond.WrapFrame(frame)
		l.send(q)
	}
}

func (l *Link) pumpClient(ctx context.Context) {
	defer l.done.Done()
	for {
		pkt := l.app.ReadContext(ctx)
		if pkt == nil {
			return
		}
		view := pkt.ToView()
		frame := append([]byte(nil), view.AsSlice()...)
		view.Release()
		pkt.DecRef()
		if ip := ethernetIPv4Payload(frame); ip != nil {
			l.deliverIP(ip)
		}
	}
}

func (l *Link) packetForFrame(frame []byte) bond.QueuedPacket {
	if ip := ethernetIPv4Payload(frame); ip != nil {
		if info, err := bond.ParseIPv4(ip); err == nil {
			l.mu.Lock()
			defer l.mu.Unlock()
			if entry, ok := l.flows[info.Flow]; ok && entry.expires.After(time.Now()) {
				return entry.packet
			}
		}
	}
	return bond.QueuedPacket{Flow: "gvisor-link-control", Priority: true, Expires: time.Now().Add(2 * time.Second)}
}

func (l *Link) pruneFlows(now time.Time) {
	for flow, entry := range l.flows {
		if !entry.expires.After(now) {
			delete(l.flows, flow)
		}
	}
	for len(l.flows) > maxFlowMetadata {
		for flow := range l.flows {
			delete(l.flows, flow)
			break
		}
	}
}

func ethernetIPv4Destination(frame []byte) netip.Addr {
	ip := ethernetIPv4Payload(frame)
	if len(ip) < 20 || ip[0]>>4 != 4 {
		return netip.Addr{}
	}
	var dst [4]byte
	copy(dst[:], ip[16:20])
	return netip.AddrFrom4(dst)
}

func ethernetIPv4Payload(frame []byte) []byte {
	if len(frame) < ethernetHeaderSize {
		return nil
	}
	typeID, offset := binary.BigEndian.Uint16(frame[12:14]), ethernetHeaderSize
	for typeID == 0x8100 || typeID == 0x88a8 {
		if len(frame) < offset+4 {
			return nil
		}
		typeID = binary.BigEndian.Uint16(frame[offset+2 : offset+4])
		offset += 4
	}
	if typeID != 0x0800 || len(frame) < offset+20 {
		return nil
	}
	return frame[offset:]
}

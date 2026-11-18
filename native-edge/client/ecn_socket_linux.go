//go:build linux

package client

import (
	"io"
	"net"
	"net/netip"

	"golang.org/x/sys/unix"
)

func enableECNReceive(conn *net.UDPConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		socketErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_RECVTOS, 1)
	}); err != nil {
		return err
	}
	return socketErr
}

func readUDPWithECN(conn *net.UDPConn, buffer []byte) (int, netip.AddrPort, byte, bool, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, netip.AddrPort{}, 0, false, err
	}
	oob := make([]byte, 128)
	var n, oobn int
	var source unix.Sockaddr
	var receiveErr error
	if err := raw.Read(func(fd uintptr) bool {
		n, oobn, _, source, receiveErr = unix.Recvmsg(int(fd), buffer, oob, unix.MSG_DONTWAIT)
		return receiveErr != unix.EAGAIN && receiveErr != unix.EWOULDBLOCK
	}); err != nil {
		return 0, netip.AddrPort{}, 0, false, err
	}
	if receiveErr != nil {
		return 0, netip.AddrPort{}, 0, false, receiveErr
	}
	peer, ok := source.(*unix.SockaddrInet4)
	if !ok {
		return 0, netip.AddrPort{}, 0, false, net.InvalidAddrError("non-IPv4 UDP peer")
	}
	var ecn byte
	hasECN := false
	if messages, parseErr := unix.ParseSocketControlMessage(oob[:oobn]); parseErr == nil {
		for _, message := range messages {
			if message.Header.Level == unix.IPPROTO_IP && message.Header.Type == unix.IP_TOS && len(message.Data) > 0 {
				ecn, hasECN = message.Data[0]&3, true
				break
			}
		}
	}
	return n, netip.AddrPortFrom(netip.AddrFrom4(peer.Addr), uint16(peer.Port)), ecn, hasECN, nil
}

func writeUDPWithECN(conn *net.UDPConn, data []byte, endpoint netip.AddrPort, trafficClass byte) error {
	if len(data) == 0 || !endpoint.IsValid() || !endpoint.Addr().Is4() {
		return net.InvalidAddrError("invalid ECN UDP destination or payload")
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	destination := &unix.SockaddrInet4{Port: int(endpoint.Port()), Addr: endpoint.Addr().As4()}
	var sent int
	var sendErr error
	if err := raw.Write(func(fd uintptr) bool {
		sendErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_TOS, int(trafficClass))
		if sendErr != nil {
			return true
		}
		sendErr = unix.Sendto(int(fd), data, unix.MSG_DONTWAIT, destination)
		if sendErr == nil {
			sent = len(data)
		}
		return sendErr != unix.EAGAIN && sendErr != unix.EWOULDBLOCK
	}); err != nil {
		return err
	}
	if sendErr != nil {
		return sendErr
	}
	if sent != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func writeUDPWithDSCP(conn *net.UDPConn, data []byte, endpoint netip.AddrPort, dscp byte) error {
	return writeUDPWithECN(conn, data, endpoint, dscp&0xfc)
}

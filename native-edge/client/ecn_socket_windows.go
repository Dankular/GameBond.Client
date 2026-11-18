//go:build windows

package client

import (
	"errors"
	"io"
	"math/bits"
	"net"
	"net/netip"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows exposes ECN as the IP_RECVECN socket option and IP_ECN control
// message, separate from the TOS socket option.
const windowsIPRecvECN = 50

func enableECNReceive(conn *net.UDPConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		socketErr = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, windowsIPRecvECN, 1)
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
	if len(buffer) == 0 {
		return 0, netip.AddrPort{}, 0, false, net.InvalidAddrError("empty UDP receive buffer")
	}
	control := make([]byte, 128)
	var name syscall.RawSockaddrAny
	dataBuf := windows.WSABuf{Len: uint32(len(buffer)), Buf: &buffer[0]}
	controlBuf := windows.WSABuf{Len: uint32(len(control)), Buf: &control[0]}
	message := windows.WSAMsg{Name: &name, Namelen: int32(unsafe.Sizeof(name)), Buffers: &dataBuf, BufferCount: 1, Control: controlBuf}
	var received uint32
	var receiveErr error
	if err := raw.Read(func(fd uintptr) bool {
		receiveErr = windows.WSARecvMsg(windows.Handle(fd), &message, &received, nil, nil)
		return !errors.Is(receiveErr, windows.WSAEWOULDBLOCK)
	}); err != nil {
		return 0, netip.AddrPort{}, 0, false, err
	}
	if receiveErr != nil {
		return 0, netip.AddrPort{}, 0, false, receiveErr
	}
	peer := (*windows.RawSockaddrInet4)(unsafe.Pointer(&name))
	if peer.Family != windows.AF_INET {
		return 0, netip.AddrPort{}, 0, false, net.InvalidAddrError("non-IPv4 UDP peer")
	}
	endpoint := netip.AddrPortFrom(netip.AddrFrom4(peer.Addr), bits.ReverseBytes16(peer.Port))
	var ecn byte
	hasECN := false
	if message.Control.Len >= uint32(unsafe.Sizeof(windows.WSACMSGHDR{})) {
		header := (*windows.WSACMSGHDR)(unsafe.Pointer(&control[0]))
		alignment := unsafe.Sizeof(uintptr(0))
		dataOffset := (unsafe.Sizeof(*header) + alignment - 1) &^ (alignment - 1)
		if header.Level == windows.IPPROTO_IP && header.Type == windowsIPRecvECN && header.Len >= dataOffset+4 && message.Control.Len >= uint32(dataOffset+4) {
			ecn, hasECN = byte(*(*uint32)(unsafe.Pointer(&control[dataOffset]))&3), true
		}
	}
	return int(received), endpoint, ecn, hasECN, nil
}

func writeUDPWithECN(conn *net.UDPConn, data []byte, endpoint netip.AddrPort, trafficClass byte) error {
	if len(data) == 0 || !endpoint.IsValid() || !endpoint.Addr().Is4() {
		return net.InvalidAddrError("ECN UDP destination must be IPv4")
	}
	// Winsock reserves CE for the network and rejects an application send with
	// ECN=3. ECT(0) is safe here: RFC 6040 decapsulation still preserves an
	// already-CE inner packet when it combines the headers.
	ecn := trafficClass & 3
	if ecn == 3 {
		ecn = 2
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var address syscall.RawSockaddrInet4
	address.Family = windows.AF_INET
	address.Port = bits.ReverseBytes16(endpoint.Port())
	address.Addr = endpoint.Addr().As4()
	control := make([]byte, 64)
	dataBuf := windows.WSABuf{Len: uint32(len(data)), Buf: &data[0]}
	alignment := unsafe.Sizeof(uintptr(0))
	headerSize := unsafe.Sizeof(windows.WSACMSGHDR{})
	dataOffset := (headerSize + alignment - 1) &^ (alignment - 1)
	controlLength := dataOffset + ((4 + alignment - 1) &^ (alignment - 1))
	controlBuf := windows.WSABuf{Len: uint32(controlLength), Buf: &control[0]}
	header := (*windows.WSACMSGHDR)(unsafe.Pointer(&control[0]))
	header.Len = dataOffset + 4
	header.Level = windows.IPPROTO_IP
	header.Type = windowsIPRecvECN
	*(*uint32)(unsafe.Pointer(&control[dataOffset])) = uint32(ecn)
	message := windows.WSAMsg{
		Name:        (*syscall.RawSockaddrAny)(unsafe.Pointer(&address)),
		Namelen:     int32(unsafe.Sizeof(address)),
		Buffers:     &dataBuf,
		BufferCount: 1,
		Control:     controlBuf,
	}
	var sent uint32
	var sendErr error
	if err := raw.Write(func(fd uintptr) bool {
		sendErr = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, windows.IP_TOS, int(trafficClass&0xfc))
		if sendErr != nil {
			return true
		}
		sendErr = windows.WSASendMsg(windows.Handle(fd), &message, 0, &sent, nil, nil)
		return !errors.Is(sendErr, windows.WSAEWOULDBLOCK)
	}); err != nil {
		return err
	}
	if sendErr != nil {
		return sendErr
	}
	if int(sent) != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func writeUDPWithDSCP(conn *net.UDPConn, data []byte, endpoint netip.AddrPort, dscp byte) error {
	if dscp&3 != 0 {
		return net.InvalidAddrError("DSCP value contains ECN bits")
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		socketErr = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, windows.IP_TOS, int(dscp))
	}); err != nil {
		return err
	}
	if socketErr != nil {
		return socketErr
	}
	_, err = conn.WriteToUDPAddrPort(data, endpoint)
	return err
}

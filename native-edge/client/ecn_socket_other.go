//go:build !windows && !linux

package client

import (
	"errors"
	"net"
	"net/netip"
)

func enableECNReceive(*net.UDPConn) error {
	return errors.New("RFC 6040 outer ECN metadata is unsupported on this platform")
}

func readUDPWithECN(*net.UDPConn, []byte) (int, netip.AddrPort, byte, bool, error) {
	return 0, netip.AddrPort{}, 0, false, errors.New("RFC 6040 outer ECN metadata is unsupported on this platform")
}

func writeUDPWithECN(*net.UDPConn, []byte, netip.AddrPort, byte) error {
	return errors.New("RFC 6040 outer ECN marking is unsupported on this platform")
}

func writeUDPWithDSCP(conn *net.UDPConn, data []byte, endpoint netip.AddrPort, _ byte) error {
	_, err := conn.WriteToUDPAddrPort(data, endpoint)
	return err
}

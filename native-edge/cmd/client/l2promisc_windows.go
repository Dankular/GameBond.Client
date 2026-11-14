package main

import (
	"context"
	"net/netip"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sioRcvAll is SIO_RCVALL = _WSAIOW(IOC_VENDOR, 1).
const sioRcvAll = 0x98000001

// holdPromiscuous keeps the bridged adapter's hardware receive filter open. The bridge speaks as
// virtual MACs (the exit gateway's 02:47:42:..), and a NIC drops unicast frames for any MAC it does
// not own, so without this the devices' ARP and DHCP broadcasts arrive but every unicast frame
// toward the gateway is lost on the wire side. SIO_RCVALL on a raw socket bound to the adapter makes
// the miniport turn promiscuous mode on, the same switch a packet sniffer flips.
//
// One socket is held for as long as the link stays up with the same address. It is opened again only
// after the link has gone down (an adapter restart clears the hardware filter) or the address has
// changed; opening and closing sockets on a timer reprograms the receive filter of a USB adapter
// over and over, which is not something to do to a live bridge.
func holdPromiscuous(ctx context.Context, interfaceID string, out func(map[string]any)) {
	var fd windows.Handle = windows.InvalidHandle
	var heldIP netip.Addr
	closeHeld := func() {
		if fd != windows.InvalidHandle {
			windows.Closesocket(fd)
			fd = windows.InvalidHandle
		}
		l2Promiscuous.Store(false)
	}
	defer closeHeld()
	lastErr := ""
	fail := func(err error) {
		l2Promiscuous.Store(false)
		l2PromiscuousWhy.Store(err.Error())
		if err.Error() != lastErr {
			out(map[string]any{"status": "l2_promiscuous_failed", "reason": err.Error()})
		}
		lastErr = err.Error()
	}
	for ctx.Err() == nil {
		ip, up, ok := adapterState(interfaceID)
		switch {
		case !ok || !up:
			closeHeld()
			fail(windows.WSAENETDOWN)
		case fd != windows.InvalidHandle && ip == heldIP:
			// Held and unchanged: nothing to do.
		case !ip.IsValid():
			closeHeld()
			fail(windows.WSAEADDRNOTAVAIL)
		default:
			closeHeld()
			if h, err := rcvAllOn(ip); err != nil {
				fail(err)
			} else {
				fd, heldIP = h, ip
				lastErr = ""
				l2Promiscuous.Store(true)
				l2PromiscuousWhy.Store("")
				out(map[string]any{"status": "l2_promiscuous_on", "address": ip.String()})
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// adapterState returns the adapter's first IPv4 address and whether its link is operationally up.
func adapterState(guid string) (ip netip.Addr, up, ok bool) {
	var size uint32 = 16 * 1024
	for i := 0; i < 3; i++ {
		buf := make([]byte, size)
		aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_INET, windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_DNS_SERVER, 0, aa, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return netip.Addr{}, false, false
		}
		for ; aa != nil; aa = aa.Next {
			if !strings.EqualFold(strings.Trim(windows.BytePtrToString(aa.AdapterName), "{}"), strings.Trim(guid, "{}")) {
				continue
			}
			up = aa.OperStatus == windows.IfOperStatusUp
			for ua := aa.FirstUnicastAddress; ua != nil; ua = ua.Next {
				if sa := ua.Address.Sockaddr; sa != nil && sa.Addr.Family == windows.AF_INET {
					a4 := (*windows.RawSockaddrInet4)(unsafe.Pointer(sa)).Addr
					return netip.AddrFrom4(a4), up, true
				}
			}
			return netip.Addr{}, up, true
		}
		return netip.Addr{}, false, false
	}
	return netip.Addr{}, false, false
}

func rcvAllOn(ip netip.Addr) (windows.Handle, error) {
	fd, err := windows.Socket(windows.AF_INET, windows.SOCK_RAW, windows.IPPROTO_IP)
	if err != nil {
		return windows.InvalidHandle, err
	}
	if err := windows.Bind(fd, &windows.SockaddrInet4{Addr: ip.As4()}); err != nil {
		windows.Closesocket(fd)
		return windows.InvalidHandle, err
	}
	on := uint32(1) // RCVALL_ON
	var n uint32
	if err := windows.WSAIoctl(fd, sioRcvAll, (*byte)(unsafe.Pointer(&on)), 4, nil, 0, &n, nil, 0); err != nil {
		windows.Closesocket(fd)
		return windows.InvalidHandle, err
	}
	// Copies queue on the socket until it is closed; a tiny receive buffer makes Windows drop them.
	windows.SetsockoptInt(fd, windows.SOL_SOCKET, windows.SO_RCVBUF, 4096)
	return fd, nil
}

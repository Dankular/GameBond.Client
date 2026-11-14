package main

import (
	"context"
	"net/netip"
	"syscall"
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
func holdPromiscuous(ctx context.Context, interfaceID string, out func(map[string]any)) {
	var prev windows.Handle = windows.InvalidHandle
	defer func() {
		if prev != windows.InvalidHandle {
			windows.Closesocket(prev)
		}
	}()
	lastErr := ""
	for ctx.Err() == nil {
		// An adapter restart clears the hardware filter, so the request is repeated on a fresh socket
		// every few seconds; the old socket is closed only after the new one is on.
		fd, err := rcvAllOn(interfaceID)
		if err != nil {
			if err.Error() != lastErr {
				out(map[string]any{"status": "l2_promiscuous_failed", "reason": err.Error()})
			}
			lastErr = err.Error()
		} else {
			lastErr = ""
			if prev != windows.InvalidHandle {
				windows.Closesocket(prev)
			}
			prev = fd
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func adapterIPv4(guid string) (netip.Addr, bool) {
	var size uint32 = 16 * 1024
	for i := 0; i < 3; i++ {
		buf := make([]byte, size)
		aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_INET, windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_DNS_SERVER, 0, aa, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return netip.Addr{}, false
		}
		for ; aa != nil; aa = aa.Next {
			if !strings.EqualFold(strings.Trim(windows.BytePtrToString(aa.AdapterName), "{}"), strings.Trim(guid, "{}")) {
				continue
			}
			for ua := aa.FirstUnicastAddress; ua != nil; ua = ua.Next {
				if sa := ua.Address.Sockaddr; sa != nil && sa.Addr.Family == windows.AF_INET {
					return netip.AddrFrom4((*syscall.RawSockaddrInet4)(unsafe.Pointer(sa)).Addr), true
				}
			}
		}
		return netip.Addr{}, false
	}
	return netip.Addr{}, false
}

func rcvAllOn(interfaceID string) (windows.Handle, error) {
	ip, ok := adapterIPv4(interfaceID)
	if !ok {
		return windows.InvalidHandle, windows.WSAEADDRNOTAVAIL
	}
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
	// Copies queue on the socket until it is closed; a small receive buffer makes Windows drop them.
	windows.SetsockoptInt(fd, windows.SOL_SOCKET, windows.SO_RCVBUF, 4096)
	return fd, nil
}

package main

import (
	"context"
	"net/netip"
	"strings"
	"syscall"
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
	for ctx.Err() == nil {
		err := rcvAllOnce(ctx, interfaceID)
		if err != nil {
			out(map[string]any{"status": "l2_promiscuous_failed", "error": err.Error()})
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
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
			if !strings.EqualFold(windows.BytePtrToString(aa.AdapterName), strings.Trim(guid, "{}")) {
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

func rcvAllOnce(ctx context.Context, interfaceID string) error {
	ip, ok := adapterIPv4(interfaceID)
	if !ok {
		return windows.WSAEADDRNOTAVAIL
	}
	fd, err := windows.Socket(windows.AF_INET, windows.SOCK_RAW, windows.IPPROTO_IP)
	if err != nil {
		return err
	}
	defer windows.Closesocket(fd)
	if err := windows.Bind(fd, &windows.SockaddrInet4{Addr: ip.As4()}); err != nil {
		return err
	}
	on := uint32(1) // RCVALL_ON
	var n uint32
	if err := windows.WSAIoctl(fd, sioRcvAll, (*byte)(unsafe.Pointer(&on)), 4, nil, 0, &n, nil, 0); err != nil {
		return err
	}
	// The socket must stay open while the bridge runs; drain it so Windows never queues the copies.
	go func() { <-ctx.Done(); windows.Closesocket(fd) }()
	buf := make([]byte, 2048)
	for ctx.Err() == nil {
		if _, _, err := windows.Recvfrom(fd, buf, 0); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
	return nil
}

// Package netstack dials TCP and UDP endpoints through a gVisor netstack fed
// by the VPN tunnel, so no TUN device or elevated privileges are needed.
package netstack

import (
	"context"
	"net"
	"net/netip"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// Network implements the dialling side of the SOCKS5 server.
type Network struct {
	Stack *stack.Stack
}

// DialTCP opens a TCP connection to dst inside the tunnel.
func (n *Network) DialTCP(ctx context.Context, dst netip.AddrPort) (net.Conn, error) {
	return gonet.DialContextTCP(ctx, n.Stack, fullAddress(dst), ipv4.ProtocolNumber)
}

// DialUDP opens a connected UDP socket to dst inside the tunnel.
func (n *Network) DialUDP(dst netip.AddrPort) (net.Conn, error) {
	conn, err := gonet.DialUDP(n.Stack, nil, &tcpip.FullAddress{
		Addr: tcpip.AddrFromSlice(dst.Addr().AsSlice()),
		Port: dst.Port(),
	}, ipv4.ProtocolNumber)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func fullAddress(dst netip.AddrPort) tcpip.FullAddress {
	return tcpip.FullAddress{
		Addr: tcpip.AddrFromSlice(dst.Addr().AsSlice()),
		Port: dst.Port(),
	}
}

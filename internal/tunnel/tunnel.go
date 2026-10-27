// Package tunnel bridges the VPN TLS stream and a user-space TCP/IP stack.
//
// Raw IPv4 packets read from the gateway are injected into a gVisor netstack
// so that ordinary sockets can be dialled out of the tunnel, without TUN
// devices, root privileges or host route changes.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"

	"github.com/sitdownkevin/tj-tunnel/internal/protocol"
)

// MTU matches the value the original client requests.
const MTU = 1280

// Control commands carried by protocol-255 packets.
const (
	cmdPing      = 1 // heartbeat
	cmdTerminate = 2 // gateway closed the session
	cmdProbe     = 4 // gateway liveness probe
	cmdAck       = 5
)

// Tunnel owns the gVisor stack fed by a VPN session.
type Tunnel struct {
	Stack   *stack.Stack
	Ep      *channel.Endpoint
	LocalIP string

	// Done receives the reason the tunnel stopped.
	Done chan error

	gw       *protocol.Session
	writeMu  sync.Mutex
	stopOnce sync.Once
}

// Start wires a freshly logged-in session to a new netstack instance.
func Start(ctx context.Context, s *protocol.Session) (*Tunnel, error) {
	ip, err := s.ClientIPv4()
	if err != nil {
		return nil, fmt.Errorf("gateway did not report a client IPv4 address: %w", err)
	}
	st, ep, err := newStack(ip)
	if err != nil {
		return nil, err
	}
	t := &Tunnel{Stack: st, Ep: ep, gw: s, LocalIP: ip, Done: make(chan error, 1)}
	interval := s.KeepaliveInterval(30 * time.Second)
	go t.pumpOutbound(ctx)
	go t.pumpInbound(ctx, interval)
	return t, nil
}

func newStack(localIP string) (*stack.Stack, *channel.Endpoint, error) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol4},
	})
	ep := channel.New(256, MTU, "")
	if err := s.CreateNIC(1, ep); err != nil {
		return nil, nil, fmt.Errorf("create NIC: %v", err)
	}
	v4 := net.ParseIP(localIP).To4()
	if v4 == nil {
		return nil, nil, fmt.Errorf("invalid client IP %q", localIP)
	}
	addr := tcpip.AddrFromSlice(v4)
	if err := s.AddProtocolAddress(1, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: addr.WithPrefix(),
	}, stack.AddressProperties{}); err != nil {
		return nil, nil, fmt.Errorf("add protocol address: %v", err)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: 1}})
	return s, ep, nil
}

// Logout closes the web session on the gateway.
func (t *Tunnel) Logout() { _ = t.gw.Gateway.Logout(t.gw.Cookie) }

// Close tears the tunnel down locally.
func (t *Tunnel) Close() { t.stop(context.Canceled) }

func (t *Tunnel) writeGateway(p []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_ = t.gw.Conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, err := t.gw.Conn.Write(p)
	return err
}

// pumpInbound feeds payload packets into the stack and answers control packets.
func (t *Tunnel) pumpInbound(ctx context.Context, interval time.Duration) {
	defer t.stop(nil)

	packets := make(chan []byte, 256)
	go func() {
		defer close(packets)
		for {
			// Give up only when the gateway stays silent well past a heartbeat.
			_ = t.gw.Conn.SetReadDeadline(time.Now().Add(4*interval + 10*time.Second))
			p, err := protocol.ReadPacket(t.gw.Reader)
			if err != nil {
				t.stop(err)
				return
			}
			if protocol.IsControl(p) {
				cmd, param := protocol.Command(p)
				switch cmd {
				case cmdPing:
					if err := t.writeGateway(protocol.ATP(cmdPing, 0)); err != nil {
						t.stop(err)
						return
					}
				case cmdTerminate:
					t.stop(fmt.Errorf("gateway terminated the session (reason %d)", param))
					return
				case cmdProbe:
					if err := t.writeGateway(protocol.ATP(cmdAck, 0)); err != nil {
						t.stop(err)
						return
					}
				}
				continue
			}
			select {
			case packets <- p:
			case <-ctx.Done():
				return
			}
		}
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.stop(ctx.Err())
			return
		case err := <-t.Done:
			t.stop(err)
			return
		case <-ticker.C:
			if err := t.writeGateway(protocol.ATP(cmdPing, 0)); err != nil {
				t.stop(err)
				return
			}
		case p, ok := <-packets:
			if !ok {
				return
			}
			pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(p)})
			t.Ep.InjectInbound(ipv4.ProtocolNumber, pkt)
		}
	}
}

// pumpOutbound writes packets emitted by the stack to the gateway.
func (t *Tunnel) pumpOutbound(ctx context.Context) {
	for {
		pkt := t.Ep.ReadContext(ctx)
		if pkt == nil {
			t.stop(ctx.Err())
			return
		}
		b := append([]byte(nil), pkt.ToView().AsSlice()...)
		pkt.DecRef()
		if err := t.writeGateway(b); err != nil {
			t.stop(err)
			return
		}
	}
}

func (t *Tunnel) stop(err error) {
	t.stopOnce.Do(func() {
		if err == nil || errors.Is(err, context.Canceled) {
			err = errors.New("connection closed by peer")
		}
		_ = t.gw.Conn.Close()
		t.Done <- err
		close(t.Done)
	})
}

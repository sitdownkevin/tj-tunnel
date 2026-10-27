// Package resolver resolves host names through the DNS servers pushed by the
// VPN gateway. Queries travel inside the tunnel (DNS over TCP) so that
// campus-only names resolve correctly.
package resolver

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

const (
	maxTTL     = 300 * time.Second
	defaultTTL = 30 * time.Second
	queryWait  = 10 * time.Second
	attempts   = 3
)

// Resolver resolves names inside the tunnel and caches the answers.
type Resolver struct {
	stack  *stack.Stack
	server netip.Addr

	mu    sync.Mutex
	cache map[string]entry
}

type entry struct {
	addr netip.Addr
	exp  time.Time
}

// New creates a resolver for the given VPN DNS servers.
func New(s *stack.Stack, servers []netip.Addr) (*Resolver, error) {
	if len(servers) == 0 {
		return nil, errors.New("gateway did not push any DNS server")
	}
	return &Resolver{stack: s, server: servers[0], cache: make(map[string]entry)}, nil
}

// Lookup returns the IPv4 address of host.
func (r *Resolver) Lookup(ctx context.Context, host string) (netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		if !addr.Is4() && !addr.Is4In6() {
			return netip.Addr{}, fmt.Errorf("IPv6 targets are not supported: %s", host)
		}
		return addr.Unmap(), nil
	}

	if addr, ok := r.cached(host); ok {
		return addr, nil
	}

	name, err := dnsmessage.NewName(host + ".")
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid host name %q: %w", host, err)
	}
	addr, ttl, err := r.query(ctx, dnsmessage.Question{
		Name:  name,
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
	})
	if err != nil {
		return netip.Addr{}, fmt.Errorf("resolve %s: %w", host, err)
	}
	r.store(host, addr, ttl)
	return addr, nil
}

func (r *Resolver) cached(host string) (netip.Addr, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.cache[host]
	if !ok || time.Now().After(e.exp) {
		return netip.Addr{}, false
	}
	return e.addr, true
}

func (r *Resolver) store(host string, addr netip.Addr, ttl time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[host] = entry{addr: addr, exp: time.Now().Add(ttl)}
}

// query retries: the gateway occasionally resets concurrent DNS-over-TCP
// connections.
func (r *Resolver) query(ctx context.Context, q dnsmessage.Question) (netip.Addr, time.Duration, error) {
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return netip.Addr{}, 0, ctx.Err()
			case <-time.After(time.Duration(100<<i) * time.Millisecond):
			}
		}
		addr, ttl, err := r.queryOnce(ctx, q)
		if err == nil {
			return addr, ttl, nil
		}
		last = err
	}
	return netip.Addr{}, 0, last
}

func (r *Resolver) queryOnce(ctx context.Context, q dnsmessage.Question) (netip.Addr, time.Duration, error) {
	b := dnsmessage.NewBuilder(make([]byte, 0, 512), dnsmessage.Header{
		ID:               uint16(rand.Intn(0xffff)),
		RecursionDesired: true,
	})
	if err := b.StartQuestions(); err != nil {
		return netip.Addr{}, 0, err
	}
	if err := b.Question(q); err != nil {
		return netip.Addr{}, 0, err
	}
	msg, err := b.Finish()
	if err != nil {
		return netip.Addr{}, 0, err
	}

	conn, err := gonet.DialContextTCP(ctx, r.stack, tcpip.FullAddress{
		Addr: tcpip.AddrFromSlice(r.server.AsSlice()),
		Port: 53,
	}, ipv4.ProtocolNumber)
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("dial VPN DNS %s: %v", r.server, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(queryWait)); err != nil {
		return netip.Addr{}, 0, err
	}

	wire := make([]byte, 2+len(msg))
	binary.BigEndian.PutUint16(wire, uint16(len(msg)))
	copy(wire[2:], msg)
	if _, err := conn.Write(wire); err != nil {
		return netip.Addr{}, 0, err
	}

	var size [2]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return netip.Addr{}, 0, err
	}
	payload := make([]byte, binary.BigEndian.Uint16(size[:]))
	if _, err := io.ReadFull(conn, payload); err != nil {
		return netip.Addr{}, 0, err
	}

	var p dnsmessage.Parser
	if _, err := p.Start(payload); err != nil {
		return netip.Addr{}, 0, err
	}
	if err := p.SkipAllQuestions(); err != nil {
		return netip.Addr{}, 0, err
	}
	for {
		ans, err := p.Answer()
		if err != nil {
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				break
			}
			return netip.Addr{}, 0, err
		}
		a, ok := ans.Body.(*dnsmessage.AResource)
		if !ok {
			continue
		}
		ttl := defaultTTL
		if ans.Header.TTL > 0 {
			ttl = min(time.Duration(ans.Header.TTL)*time.Second, maxTTL)
		}
		return netip.AddrFrom4(a.A), ttl, nil
	}
	return netip.Addr{}, 0, errors.New("no A record in response")
}

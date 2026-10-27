package socks

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	udpMaxDatagram = 65535
	udpIdleTimeout = 90 * time.Second
	udpJanitorTick = 30 * time.Second
	udpMaxLookups  = 128
)

// UDPRelay implements the relay side of UDP ASSOCIATE: a single socket
// receives encapsulated datagrams from every client, forwards the payload
// into the tunnel and sends the answers back.
type UDPRelay struct {
	conn *net.UDPConn
	addr netip.AddrPort // advertised as BND.ADDR:BND.PORT

	resolver Resolver
	dialer   Dialer
	log      *slog.Logger
	counters *Counters

	mu     sync.Mutex
	assocs map[*Association]struct{}

	lookups chan struct{} // bounds concurrent DNS lookups
}

// NewUDPRelay binds the relay socket. advertise is the address returned to
// clients; when its host is 0.0.0.0 clients substitute the proxy address.
func NewUDPRelay(bind, advertise string, r Resolver, d Dialer, log *slog.Logger, c *Counters) (*UDPRelay, error) {
	laddr, err := net.ResolveUDPAddr("udp", bind)
	if err != nil {
		return nil, err
	}
	// The tunnel only carries IPv4, and a UDP4 socket keeps source addresses
	// comparable with the TCP peer address (a dual-stack socket reports IPv4
	// peers as ::ffff:a.b.c.d).
	network := "udp"
	if laddr.IP == nil || laddr.IP.To4() != nil {
		network = "udp4"
	}
	conn, err := net.ListenUDP(network, laddr)
	if err != nil {
		return nil, err
	}
	adv, err := netip.ParseAddrPort(advertise)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &UDPRelay{
		conn:     conn,
		addr:     adv,
		resolver: r,
		dialer:   d,
		log:      log,
		counters: c,
		assocs:   make(map[*Association]struct{}),
		lookups:  make(chan struct{}, udpMaxLookups),
	}, nil
}

// Advertised returns the BND address handed to clients.
func (r *UDPRelay) Advertised() netip.AddrPort { return r.addr }

// Close stops the relay socket.
func (r *UDPRelay) Close() error { return r.conn.Close() }

// Run reads datagrams until ctx is cancelled.
func (r *UDPRelay) Run(ctx context.Context) {
	go func() {
		<-ctx.Done()
		r.conn.Close()
	}()
	go r.janitor(ctx)

	buf := make([]byte, udpMaxDatagram)
	for {
		n, src, err := r.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				r.log.Warn("UDP relay read failed", "err", err)
			}
			return
		}
		// RSV(2) + FRAG(1) + ATYP(1) + IPv4(4) + PORT(2)
		if n < 10 || buf[2] != 0 { // fragmented datagrams are not supported
			continue
		}
		a := r.assocFor(src)
		if a == nil {
			continue // no association for this source
		}
		a.handle(append([]byte(nil), buf[:n]...))
	}
}

// Add registers an association for the given client IP.
func (r *UDPRelay) Add(peerIP netip.Addr) *Association {
	a := &Association{relay: r, peerIP: peerIP, dests: make(map[netip.AddrPort]*dest)}
	r.mu.Lock()
	r.assocs[a] = struct{}{}
	r.mu.Unlock()
	return a
}

// Remove drops an association and closes its sockets.
func (r *UDPRelay) Remove(a *Association) {
	r.mu.Lock()
	delete(r.assocs, a)
	r.mu.Unlock()
	a.close()
}

// assocFor finds the association owning src, binding a pending one on the
// first datagram (clients usually send 0.0.0.0:0 in the request).
func (r *UDPRelay) assocFor(src netip.AddrPort) *Association {
	src = netip.AddrPortFrom(src.Addr().Unmap(), src.Port())
	r.mu.Lock()
	defer r.mu.Unlock()
	for a := range r.assocs {
		if a.clientAddr() == src {
			return a
		}
	}
	for a := range r.assocs {
		if a.bind(src) {
			return a
		}
	}
	return nil
}

func (r *UDPRelay) janitor(ctx context.Context) {
	ticker := time.NewTicker(udpJanitorTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			r.mu.Lock()
			all := make([]*Association, 0, len(r.assocs))
			for a := range r.assocs {
				all = append(all, a)
			}
			r.mu.Unlock()
			for _, a := range all {
				a.expire(now)
			}
		}
	}
}

// Association is one client's UDP association.
type Association struct {
	relay  *UDPRelay
	peerIP netip.Addr

	mu     sync.Mutex
	client *netip.AddrPort
	dests  map[netip.AddrPort]*dest
	closed bool
}

type dest struct {
	conn net.Conn
	last time.Time
}

func (a *Association) clientAddr() netip.AddrPort {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client == nil {
		return netip.AddrPort{}
	}
	return *a.client
}

// bind attaches src to this association if it is still unbound and comes
// from the expected client IP.
func (a *Association) bind(src netip.AddrPort) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client != nil || a.closed || a.peerIP != src.Addr() {
		return false
	}
	a.client = &src
	return true
}

// handle processes one decapsulated datagram.
func (a *Association) handle(pkt []byte) {
	host, port, n, err := parseAddr(pkt[3:])
	if err != nil {
		return
	}
	payload := pkt[3+n:]
	if len(payload) == 0 {
		return
	}
	go a.forward(host, port, payload)
}

func (a *Association) forward(host string, port uint16, payload []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	select {
	case a.relay.lookups <- struct{}{}:
		defer func() { <-a.relay.lookups }()
	case <-ctx.Done():
		return
	}

	addr, err := a.relay.resolver.Lookup(ctx, host)
	if err != nil {
		return
	}
	dst := netip.AddrPortFrom(addr, port)
	d := a.dest(dst)
	if d == nil {
		return
	}
	if _, err := d.conn.Write(payload); err != nil {
		a.drop(dst)
		return
	}
	if a.relay.counters != nil {
		a.relay.counters.Up.Add(int64(len(payload)))
	}
}

// dest returns the tunnel socket for dst, creating it on first use.
func (a *Association) dest(dst netip.AddrPort) *dest {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	if d, ok := a.dests[dst]; ok {
		d.last = time.Now()
		return d
	}
	conn, err := a.relay.dialer.DialUDP(dst)
	if err != nil {
		a.relay.log.Debug("UDP dial failed", "dst", dst, "err", err)
		return nil
	}
	d := &dest{conn: conn, last: time.Now()}
	a.dests[dst] = d
	go a.readLoop(dst, d)
	return d
}

func (a *Association) drop(dst netip.AddrPort) {
	a.mu.Lock()
	if d, ok := a.dests[dst]; ok {
		d.conn.Close()
		delete(a.dests, dst)
	}
	a.mu.Unlock()
}

func (a *Association) expire(now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for dst, d := range a.dests {
		if now.Sub(d.last) > udpIdleTimeout {
			d.conn.Close()
			delete(a.dests, dst)
		}
	}
}

func (a *Association) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	for dst, d := range a.dests {
		d.conn.Close()
		delete(a.dests, dst)
	}
}

// readLoop forwards answers from the tunnel back to the client.
func (a *Association) readLoop(src netip.AddrPort, d *dest) {
	buf := make([]byte, udpMaxDatagram)
	for {
		n, err := d.conn.Read(buf)
		if err != nil {
			a.drop(src)
			return
		}
		a.mu.Lock()
		client, closed := a.client, a.closed
		if !closed {
			d.last = time.Now()
		}
		a.mu.Unlock()
		if closed || client == nil {
			return
		}
		out := make([]byte, 0, 10+n)
		out = append(out, 0, 0, 0) // RSV, RSV, FRAG
		out = append(out, encodeAddr(src)...)
		out = append(out, buf[:n]...)
		if _, err := a.relay.conn.WriteToUDPAddrPort(out, *client); err != nil {
			return
		}
		if a.relay.counters != nil {
			a.relay.counters.Down.Add(int64(n))
		}
	}
}

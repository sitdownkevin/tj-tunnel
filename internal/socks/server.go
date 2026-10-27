package socks

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync/atomic"
	"time"
)

// Resolver turns a host name into an IPv4 address. IP literals must be
// returned unchanged.
type Resolver interface {
	Lookup(ctx context.Context, host string) (netip.Addr, error)
}

// Dialer opens connections through the VPN tunnel.
type Dialer interface {
	DialTCP(ctx context.Context, dst netip.AddrPort) (net.Conn, error)
	DialUDP(dst netip.AddrPort) (net.Conn, error)
}

// Auth enables RFC 1929 username/password authentication when non-nil.
type Auth struct {
	Username string
	Password string
}

// Counters track aggregate traffic for logging.
type Counters struct {
	Connections atomic.Int64
	Up          atomic.Int64
	Down        atomic.Int64
}

// Server is a SOCKS5 proxy front end.
type Server struct {
	Listener net.Listener
	Resolver Resolver
	Dialer   Dialer
	Auth     *Auth // nil disables authentication
	UDP      *UDPRelay
	Log      *slog.Logger
	Counters *Counters

	// HandshakeTimeout bounds greeting + request parsing.
	HandshakeTimeout time.Duration
}

// Serve accepts connections until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) {
	go func() {
		<-ctx.Done()
		s.Listener.Close()
	}()
	if s.UDP != nil {
		go s.UDP.Run(ctx)
	}
	var delay time.Duration
	for {
		c, err := s.Listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			// Transient accept failures (fd limits, ...): back off a little
			// instead of spinning, and keep serving existing connections.
			delay = min(max(2*delay, 5*time.Millisecond), time.Second)
			s.Log.Warn("accept failed", "err", err, "retry_in", delay)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			continue
		}
		delay = 0
		go s.handle(ctx, c)
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	if s.Counters != nil {
		s.Counters.Connections.Add(1)
		defer s.Counters.Connections.Add(-1)
	}
	timeout := s.HandshakeTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))

	if err := s.authenticate(conn); err != nil {
		if !errors.Is(err, errNoAcceptableMethod) {
			s.Log.Debug("handshake failed", "client", conn.RemoteAddr(), "err", err)
		}
		return
	}
	req, err := readRequest(conn)
	if err != nil {
		s.Log.Debug("bad request", "client", conn.RemoteAddr(), "err", err)
		_ = writeReply(conn, replyCmdNotSupported)
		return
	}
	_ = conn.SetDeadline(time.Time{})

	switch req.cmd {
	case cmdConnect:
		s.handleConnect(ctx, conn, req)
	case cmdUDPAssociate:
		s.handleAssociate(ctx, conn, req)
	}
}

func (s *Server) handleConnect(ctx context.Context, conn net.Conn, req request) {
	addr, err := s.Resolver.Lookup(ctx, req.host)
	if err != nil {
		s.Log.Debug("resolve failed", "host", req.host, "err", err)
		_ = writeReply(conn, replyHostUnreachable)
		return
	}
	dst := netip.AddrPortFrom(addr, req.port)
	remote, err := s.Dialer.DialTCP(ctx, dst)
	if err != nil {
		s.Log.Debug("dial failed", "dst", dst, "err", err)
		_ = writeReply(conn, replyHostUnreachable)
		return
	}
	defer remote.Close()

	if !writeReply(conn, replySuccess) {
		return
	}
	up, down := relay(conn, remote)
	if s.Counters != nil {
		s.Counters.Up.Add(up)
		s.Counters.Down.Add(down)
	}
}

// handleAssociate registers a UDP association and keeps the control
// connection open until the client closes it.
func (s *Server) handleAssociate(ctx context.Context, conn net.Conn, req request) {
	if s.UDP == nil {
		s.Log.Debug("UDP ASSOCIATE refused: UDP relay disabled")
		_ = writeReply(conn, replyCmdNotSupported)
		return
	}
	peerIP := peerIP(conn, req.host)
	if !peerIP.IsValid() {
		_ = writeReply(conn, replyGeneralFailure)
		return
	}

	assoc := s.UDP.Add(peerIP)
	defer s.UDP.Remove(assoc)

	if !writeReplyAddr(conn, replySuccess, s.UDP.Advertised()) {
		return
	}
	s.Log.Debug("UDP association established", "client", peerIP, "relay", s.UDP.Advertised())

	// The association lives as long as the control connection.
	_, _ = io.Copy(io.Discard, conn)
}

// peerIP determines the client IP of an association: the address carried in
// the request when it is concrete, otherwise the TCP peer.
func peerIP(conn net.Conn, host string) netip.Addr {
	if ap, err := netip.ParseAddrPort(conn.RemoteAddr().String()); err == nil {
		if ip, err := netip.ParseAddr(host); err == nil && !ip.IsUnspecified() {
			return ip.Unmap()
		}
		return ap.Addr().Unmap()
	}
	return netip.Addr{}
}

// authenticate performs the greeting and, when configured, RFC 1929 auth.
func (s *Server) authenticate(conn net.Conn) error {
	var head [2]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return err
	}
	if head[0] != Version {
		return errors.New("not a SOCKS5 client")
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}

	want := byte(methodNoAuth)
	if s.Auth != nil {
		want = methodUserPass
	}
	chosen := byte(methodReject)
	for _, m := range methods {
		if m == want {
			chosen = want
			break
		}
	}
	if _, err := conn.Write([]byte{Version, chosen}); err != nil {
		return err
	}
	if chosen == methodReject {
		return errNoAcceptableMethod
	}
	if s.Auth == nil {
		return nil
	}
	return s.checkUserPass(conn)
}

func (s *Server) checkUserPass(conn net.Conn) error {
	var head [2]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return err
	}
	user := make([]byte, head[1])
	if _, err := io.ReadFull(conn, user); err != nil {
		return err
	}
	var plen [1]byte
	if _, err := io.ReadFull(conn, plen[:]); err != nil {
		return err
	}
	pass := make([]byte, plen[0])
	if _, err := io.ReadFull(conn, pass); err != nil {
		return err
	}
	ok := subtle.ConstantTimeCompare(user, []byte(s.Auth.Username)) == 1 &&
		subtle.ConstantTimeCompare(pass, []byte(s.Auth.Password)) == 1
	status := byte(0x01)
	if ok {
		status = 0x00
	}
	if _, err := conn.Write([]byte{0x01, status}); err != nil {
		return err
	}
	if !ok {
		return errors.New("invalid proxy credentials")
	}
	return nil
}

// relay copies bytes both ways and returns the byte counts.
func relay(a net.Conn, b net.Conn) (up, down int64) {
	type result struct {
		n   int64
		dir int
	}
	done := make(chan result, 2)
	go func() {
		n, _ := io.Copy(b, a)
		closeWrite(b)
		done <- result{n, 0}
	}()
	go func() {
		n, _ := io.Copy(a, b)
		closeWrite(a)
		done <- result{n, 1}
	}()
	for i := 0; i < 2; i++ {
		r := <-done
		if r.dir == 0 {
			up = r.n
		} else {
			down = r.n
		}
	}
	return up, down
}

// closeWrite half-closes a connection when the transport supports it.
func closeWrite(c net.Conn) {
	type closeWriter interface{ CloseWrite() error }
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

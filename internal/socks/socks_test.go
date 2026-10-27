package socks

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func newTestServer(auth *Auth) *Server {
	return &Server{Auth: auth, Log: discardLogger(), Counters: &Counters{}}
}

// newServerWithBackend returns a server whose tunnel is immediately usable.
func newServerWithBackend(res Resolver, d Dialer, udp *UDPRelay) *Server {
	s := newTestServer(nil)
	s.SetBackend(&Backend{Resolver: res, Dialer: d, UDP: udp})
	return s
}

func TestAuthenticateNoAuth(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	s := newTestServer(nil)

	done := make(chan error, 1)
	go func() { done <- s.authenticate(server) }()

	if _, err := client.Write([]byte{Version, 2, methodUserPass, methodNoAuth}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	var reply [2]byte
	if _, err := io.ReadFull(client, reply[:]); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if reply != [2]byte{Version, methodNoAuth} {
		t.Fatalf("reply = % x", reply)
	}
	if err := <-done; err != nil {
		t.Fatalf("authenticate: %v", err)
	}
}

func TestAuthenticateRejectsWhenMethodMissing(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	s := newTestServer(&Auth{Username: "u", Password: "p"})

	done := make(chan error, 1)
	go func() { done <- s.authenticate(server) }()

	if _, err := client.Write([]byte{Version, 1, methodNoAuth}); err != nil {
		t.Fatal(err)
	}
	var reply [2]byte
	if _, err := io.ReadFull(client, reply[:]); err != nil {
		t.Fatal(err)
	}
	if reply[1] != methodReject {
		t.Fatalf("reply = % x, want rejection", reply)
	}
	if err := <-done; !errors.Is(err, errNoAcceptableMethod) {
		t.Fatalf("err = %v, want errNoAcceptableMethod", err)
	}
}

func TestUserPassAuth(t *testing.T) {
	cases := []struct {
		name       string
		user, pass string
		wantStatus byte
		wantErr    bool
	}{
		{"correct", "alice", "s3cret", 0x00, false},
		{"wrong password", "alice", "nope", 0x01, true},
		{"wrong user", "bob", "s3cret", 0x01, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, client := net.Pipe()
			defer client.Close()
			s := newTestServer(&Auth{Username: "alice", Password: "s3cret"})

			done := make(chan error, 1)
			go func() { done <- s.authenticate(server) }()

			if _, err := client.Write([]byte{Version, 1, methodUserPass}); err != nil {
				t.Fatal(err)
			}
			var method [2]byte
			if _, err := io.ReadFull(client, method[:]); err != nil {
				t.Fatal(err)
			}
			if method[1] != methodUserPass {
				t.Fatalf("method = % x", method)
			}

			req := append([]byte{0x01, byte(len(tc.user))}, tc.user...)
			req = append(req, byte(len(tc.pass)))
			req = append(req, tc.pass...)
			if _, err := client.Write(req); err != nil {
				t.Fatal(err)
			}
			var status [2]byte
			if _, err := io.ReadFull(client, status[:]); err != nil {
				t.Fatal(err)
			}
			if status[1] != tc.wantStatus {
				t.Fatalf("status = % x, want % x", status, tc.wantStatus)
			}
			err := <-done
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestReadRequest(t *testing.T) {
	ipv4 := []byte{Version, cmdConnect, 0, atypIPv4, 192, 168, 80, 60, 0x01, 0xBB}
	req, err := readRequest(bytes.NewReader(ipv4))
	if err != nil {
		t.Fatalf("readRequest: %v", err)
	}
	if req.cmd != cmdConnect || req.host != "192.168.80.60" || req.port != 443 {
		t.Fatalf("req = %+v", req)
	}

	domain := []byte{Version, cmdUDPAssociate, 0, atypDomain, 3, 'a', '.', 'b', 0x00, 0x35}
	req, err = readRequest(bytes.NewReader(domain))
	if err != nil {
		t.Fatalf("readRequest: %v", err)
	}
	if req.cmd != cmdUDPAssociate || req.host != "a.b" || req.port != 53 {
		t.Fatalf("req = %+v", req)
	}
}

func TestReadRequestRejectsUnsupported(t *testing.T) {
	cases := map[string][]byte{
		"bad version": {4, cmdConnect, 0, atypIPv4, 1, 1, 1, 1, 0, 80},
		"bind cmd":    {Version, 0x02, 0, atypIPv4, 1, 1, 1, 1, 0, 80},
		"bad atyp":    {Version, cmdConnect, 0, 0x09, 1, 1, 1, 1, 0, 80},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := readRequest(bytes.NewReader(in)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestEncodeParseAddrRoundTrip(t *testing.T) {
	for _, ap := range []netip.AddrPort{
		netip.MustParseAddrPort("10.1.2.3:8080"),
		netip.MustParseAddrPort("[2001:db8::1]:443"),
	} {
		enc := encodeAddr(ap)
		host, port, n, err := parseAddr(enc)
		if err != nil {
			t.Fatalf("parseAddr(% x): %v", enc, err)
		}
		if n != len(enc) {
			t.Fatalf("consumed %d of %d bytes", n, len(enc))
		}
		got, err := netip.ParseAddrPort(net.JoinHostPort(host, itoa(port)))
		if err != nil {
			t.Fatalf("reparse: %v", err)
		}
		if got != ap {
			t.Fatalf("round trip %v -> %v", ap, got)
		}
	}
}

func itoa(p uint16) string {
	return string([]byte{byte('0' + p/10000%10), byte('0' + p/1000%10), byte('0' + p/100%10), byte('0' + p/10%10), byte('0' + p%10)})
}

func TestParseAddrRejectsTruncated(t *testing.T) {
	cases := [][]byte{
		{},
		{atypIPv4, 1, 2},
		{atypDomain, 5, 'a'},
		{atypIPv6, 1, 2},
		{0x09},
	}
	for _, in := range cases {
		if _, _, _, err := parseAddr(in); err == nil {
			t.Errorf("parseAddr(% x) should fail", in)
		}
	}
}

// fakeDialer hands out in-memory pipes instead of tunnel connections.
type fakeDialer struct {
	tcpConn net.Conn
	udpConn net.Conn
	lastDst netip.AddrPort
}

func (f *fakeDialer) DialTCP(context.Context, netip.AddrPort) (net.Conn, error) {
	return f.tcpConn, nil
}

func (f *fakeDialer) DialUDP(dst netip.AddrPort) (net.Conn, error) {
	f.lastDst = dst
	return f.udpConn, nil
}

type staticResolver map[string]netip.Addr

func (r staticResolver) Lookup(_ context.Context, host string) (netip.Addr, error) {
	if addr, ok := r[host]; ok {
		return addr, nil
	}
	return netip.Addr{}, errors.New("not found")
}

func TestConnectRelaysBytes(t *testing.T) {
	proxySide, clientSide := net.Pipe()
	defer clientSide.Close()
	remoteSide, tunnelSide := net.Pipe()
	defer tunnelSide.Close()

	dialer := &fakeDialer{tcpConn: tunnelSide}
	s := newServerWithBackend(staticResolver{"example.test": netip.MustParseAddr("10.0.0.7")}, dialer, nil)
	go s.handle(context.Background(), proxySide)

	if _, err := clientSide.Write([]byte{Version, 1, methodNoAuth}); err != nil {
		t.Fatal(err)
	}
	var method [2]byte
	if _, err := io.ReadFull(clientSide, method[:]); err != nil {
		t.Fatal(err)
	}
	req := []byte{Version, cmdConnect, 0, atypDomain, byte(len("example.test"))}
	req = append(req, "example.test"...)
	req = append(req, 0x00, 0x50)
	if _, err := clientSide.Write(req); err != nil {
		t.Fatal(err)
	}
	var reply [10]byte
	if _, err := io.ReadFull(clientSide, reply[:]); err != nil {
		t.Fatal(err)
	}
	if reply[1] != replySuccess {
		t.Fatalf("reply code = %d", reply[1])
	}

	// Client -> tunnel.
	go func() { _, _ = clientSide.Write([]byte("ping")) }()
	got := make([]byte, 4)
	_ = remoteSide.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(remoteSide, got); err != nil {
		t.Fatalf("read at remote: %v", err)
	}
	if string(got) != "ping" {
		t.Fatalf("remote got %q", got)
	}

	// Tunnel -> client.
	go func() { _, _ = remoteSide.Write([]byte("pong")) }()
	_ = clientSide.SetReadDeadline(time.Now().Add(2 * time.Second))
	back := make([]byte, 4)
	if _, err := io.ReadFull(clientSide, back); err != nil {
		t.Fatalf("read at client: %v", err)
	}
	if string(back) != "pong" {
		t.Fatalf("client got %q", back)
	}
}

func TestConnectUnknownHostRepliesUnreachable(t *testing.T) {
	proxySide, clientSide := net.Pipe()
	defer clientSide.Close()
	s := newServerWithBackend(staticResolver{}, &fakeDialer{}, nil)
	go s.handle(context.Background(), proxySide)

	_, _ = clientSide.Write([]byte{Version, 1, methodNoAuth})
	var method [2]byte
	_, _ = io.ReadFull(clientSide, method[:])
	req := []byte{Version, cmdConnect, 0, atypDomain, 7}
	req = append(req, "nope.io"...)
	req = append(req, 0x00, 0x50)
	_, _ = clientSide.Write(req)

	var reply [10]byte
	if _, err := io.ReadFull(clientSide, reply[:]); err != nil {
		t.Fatal(err)
	}
	if reply[1] != replyHostUnreachable {
		t.Fatalf("reply code = %d, want %d", reply[1], replyHostUnreachable)
	}
}

func TestAssociateWithoutRelayIsRefused(t *testing.T) {
	proxySide, clientSide := net.Pipe()
	defer clientSide.Close()
	s := newServerWithBackend(staticResolver{}, &fakeDialer{}, nil)
	go s.handle(context.Background(), proxySide)

	_, _ = clientSide.Write([]byte{Version, 1, methodNoAuth})
	var method [2]byte
	_, _ = io.ReadFull(clientSide, method[:])
	_, _ = clientSide.Write([]byte{Version, cmdUDPAssociate, 0, atypIPv4, 0, 0, 0, 0, 0, 0})

	var reply [10]byte
	if _, err := io.ReadFull(clientSide, reply[:]); err != nil {
		t.Fatal(err)
	}
	if reply[1] != replyCmdNotSupported {
		t.Fatalf("reply code = %d, want %d", reply[1], replyCmdNotSupported)
	}
}

func TestRequestWithoutTunnelFailsFast(t *testing.T) {
	proxySide, clientSide := net.Pipe()
	defer clientSide.Close()
	s := newTestServer(nil) // no backend: the VPN tunnel is down
	go s.handle(context.Background(), proxySide)

	_, _ = clientSide.Write([]byte{Version, 1, methodNoAuth})
	var method [2]byte
	if _, err := io.ReadFull(clientSide, method[:]); err != nil {
		t.Fatal(err)
	}
	req := []byte{Version, cmdConnect, 0, atypIPv4, 10, 0, 0, 1, 0x00, 0x50}
	if _, err := clientSide.Write(req); err != nil {
		t.Fatal(err)
	}

	_ = clientSide.SetReadDeadline(time.Now().Add(2 * time.Second))
	var reply [10]byte
	if _, err := io.ReadFull(clientSide, reply[:]); err != nil {
		t.Fatalf("expected an immediate reply, got %v", err)
	}
	if reply[1] != replyGeneralFailure {
		t.Fatalf("reply code = %d, want %d", reply[1], replyGeneralFailure)
	}
}

func TestAssocBindsOnlyMatchingPeerIP(t *testing.T) {
	r := &UDPRelay{log: discardLogger(), assocs: map[*Association]struct{}{}, lookups: make(chan struct{}, 1)}
	a := r.Add(netip.MustParseAddr("192.0.2.10"))

	if a.bind(netip.MustParseAddrPort("198.51.100.1:1234")) {
		t.Fatal("association must not bind to an unexpected client IP")
	}
	if !a.bind(netip.MustParseAddrPort("192.0.2.10:1234")) {
		t.Fatal("association should bind to the expected client IP")
	}
	if a.clientAddr() != netip.MustParseAddrPort("192.0.2.10:1234") {
		t.Fatalf("client = %v", a.clientAddr())
	}
}

func TestAssocReusesDestinationSocket(t *testing.T) {
	remoteSide, tunnelSide := net.Pipe()
	defer remoteSide.Close()
	defer tunnelSide.Close()

	dialer := &fakeDialer{udpConn: tunnelSide}
	r := &UDPRelay{
		dialer:   dialer,
		log:      discardLogger(),
		counters: &Counters{},
		lookups:  make(chan struct{}, 1),
		assocs:   map[*Association]struct{}{},
	}
	a := r.Add(netip.MustParseAddr("192.0.2.10"))
	a.bind(netip.MustParseAddrPort("192.0.2.10:9999"))

	dst := netip.MustParseAddrPort("10.0.0.1:53")
	d1 := a.dest(dst)
	d2 := a.dest(dst)
	if d1 == nil || d1 != d2 {
		t.Fatal("destination sockets should be reused")
	}
	if dialer.lastDst != dst {
		t.Fatalf("dialled %v, want %v", dialer.lastDst, dst)
	}

	a.expire(time.Now().Add(udpIdleTimeout + time.Second))
	if len(a.dests) != 0 {
		t.Fatal("idle destination socket should have been reaped")
	}
}

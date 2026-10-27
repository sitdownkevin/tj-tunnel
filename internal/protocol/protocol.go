// Package protocol implements the Tongji campus VPN (Sangfor SSL-VPN
// variant) login handshake and the IP-over-TLS tunnel framing.
//
// The wire format is documented in docs/protocol.md. Protocol details were
// derived from https://github.com/sitdownkevin/open_tj (MIT License).
package protocol

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Protocol555 marks control packets disguised as IPv4 with protocol 255.
const protocol555 = 255

// Credentials are the campus VPN login details.
type Credentials struct {
	Username string
	Password string
	Method   string // LDAP3 when empty
}

// Gateway is a VPN concentrator.
type Gateway struct {
	Host    string
	Address string // optional "ip:port" used when DNS is unavailable
	TLS     *tls.Config
}

// Session is an established tunnel: a TLS stream carrying raw IPv4 packets
// interleaved with control packets.
type Session struct {
	Conn    net.Conn
	Reader  *bufio.Reader
	Common  map[string]json.RawMessage
	Network map[string]json.RawMessage
	Cookie  string
	Gateway Gateway
}

// WriteAll writes p completely.
func WriteAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

// ATP builds a 20-byte control packet: an IPv4 header lookalike with
// protocol 255 carrying a command and a parameter.
func ATP(command uint16, parameter uint32) []byte {
	p := make([]byte, 20)
	p[0] = 0x45
	p[9] = protocol555
	binary.BigEndian.PutUint16(p[2:], 20)
	binary.BigEndian.PutUint16(p[12:], command)
	binary.BigEndian.PutUint32(p[16:], parameter)
	return p
}

// IsControl reports whether p is a control packet rather than payload.
func IsControl(p []byte) bool { return len(p) > 9 && p[9] == protocol555 }

// Command returns the command of a control packet and its parameter.
func Command(p []byte) (uint16, uint32) {
	if len(p) < 20 {
		return 0, 0
	}
	return binary.BigEndian.Uint16(p[12:]), binary.BigEndian.Uint32(p[16:])
}

// ReadPacket reads one IPv4 packet framed by the length field in its header.
func ReadPacket(r io.Reader) ([]byte, error) {
	h := make([]byte, 20)
	if _, err := io.ReadFull(r, h); err != nil {
		return nil, err
	}
	if h[0]>>4 != 4 || int(h[0]&15)*4 < 20 {
		return nil, errors.New("invalid IPv4 header")
	}
	n := int(binary.BigEndian.Uint16(h[2:]))
	if n < int(h[0]&15)*4 {
		return nil, fmt.Errorf("invalid IPv4 length %d", n)
	}
	p := make([]byte, n)
	copy(p, h)
	if _, err := io.ReadFull(r, p[20:]); err != nil {
		return nil, err
	}
	return p, nil
}

func readConfig(r *bufio.Reader, kind byte) (map[string]json.RawMessage, error) {
	h := make([]byte, 16)
	if _, err := io.ReadFull(r, h); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(h[12:])
	if h[0] != kind || n > 1024*1024 {
		return nil, errors.New("invalid config header")
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		return nil, err
	}
	for len(p) > 0 && p[len(p)-1] == 0 {
		p = p[:len(p)-1]
	}
	var m map[string]json.RawMessage
	err := json.Unmarshal(p, &m)
	return m, err
}

func configRequest(kind byte, payload []byte) []byte {
	p := make([]byte, 16+len(payload))
	p[0] = kind
	binary.LittleEndian.PutUint32(p[12:], uint32(len(payload)))
	copy(p[16:], payload)
	return p
}

// IPv4 decodes a gateway address value, which is either a dotted string or
// a little-endian uint32.
func IPv4(raw json.RawMessage) (string, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil && net.ParseIP(s).To4() != nil {
		return s, nil
	}
	var n uint32
	if err := json.Unmarshal(raw, &n); err != nil {
		return "", err
	}
	p := make(net.IP, 4)
	binary.LittleEndian.PutUint32(p, n)
	return p.String(), nil
}

func (g Gateway) dial(ctx context.Context) (net.Conn, error) {
	address := g.Address
	if address == "" {
		address = net.JoinHostPort(g.Host, "443")
	}
	c, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	cfg := g.TLS
	if cfg == nil {
		cfg = &tls.Config{
			ServerName: g.Host,
			MinVersion: tls.VersionTLS12,
			CipherSuites: []uint16{
				tls.TLS_RSA_WITH_AES_128_CBC_SHA,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			},
		}
	}
	t := tls.Client(c, cfg)
	if err := t.HandshakeContext(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return t, nil
}

func (g Gateway) request(ctx context.Context, path, cookie string, body url.Values) (*http.Response, error) {
	c, err := g.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	c.SetDeadline(time.Now().Add(20 * time.Second))

	method := http.MethodGet
	var reader io.Reader
	if body != nil {
		method = http.MethodPost
		reader = strings.NewReader(body.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://"+g.Host+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", "ANStandalone=true;"+cookie)
	req.Header.Set("appid", "SSPVPN")
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if err := req.Write(c); err != nil {
		return nil, err
	}
	res, err := http.ReadResponse(bufio.NewReader(c), req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20)); err != nil {
		return nil, err
	}
	return res, nil
}

// Login authenticates and upgrades the HTTPS connection into a tunnel.
func (g Gateway) Login(ctx context.Context, cred Credentials) (*Session, error) {
	if cred.Method == "" {
		cred.Method = "LDAP3"
	}
	res, err := g.request(ctx, "/prx/000/http/localhost/login", "", url.Values{
		"method":      {cred.Method},
		"uname":       {cred.Username},
		"pwd":         {cred.Password},
		"pwd1":        {""},
		"pwd2":        {""},
		"deviceid":    {""},
		"device_name": {"tjsocks"},
		"hardwareid":  {""},
	})
	if err != nil {
		return nil, err
	}
	var cookies []string
	for _, c := range res.Cookies() {
		cookies = append(cookies, c.Name+"="+c.Value)
	}
	cookie := strings.Join(cookies, "; ")
	ok := false
	defer func() {
		if !ok && cookie != "" {
			g.Logout(cookie)
		}
	}()
	if !strings.Contains(res.Header.Get("Location"), "/welcome") {
		return nil, fmt.Errorf("authentication failed (%s, HTTP %d): check username, password and auth method", cred.Method, res.StatusCode)
	}

	c, err := g.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !ok {
			c.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	c.SetDeadline(time.Now().Add(20 * time.Second))

	var id [16]byte
	_, _ = rand.Read(id[:])
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+g.Host+"/prx/000/http/localhost/vpntunnel", nil)
	if err != nil {
		return nil, err
	}
	for k, v := range map[string]string{
		"Cookie":             "ANStandalone=true;" + cookie,
		"x-devtype":          "3",
		"appid":              "SSPVPN",
		"clientid":           hex.EncodeToString(id[:]),
		"cpuid":              "tjsocks",
		"hostname":           "tjsocks",
		"PAYLOAD-IP-VERSION": "6",
	} {
		req.Header.Set(k, v)
	}
	if err := req.Write(c); err != nil {
		return nil, err
	}

	r := bufio.NewReaderSize(c, 65536)
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.Contains(line, " 201 ") {
		return nil, fmt.Errorf("unsupported tunnel response: %s", strings.TrimSpace(line))
	}
	for total := len(line); ; {
		line, err = r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		total += len(line)
		if total > 65536 {
			return nil, errors.New("oversized tunnel header")
		}
		if line == "\r\n" {
			break
		}
	}

	// Ask for the common and network configuration blocks.
	if err := WriteAll(c, configRequest(0x50, nil)); err != nil {
		return nil, err
	}
	common, err := readConfig(r, 0x51)
	if err != nil {
		return nil, err
	}
	if err := WriteAll(c, configRequest(0x54, make([]byte, 32))); err != nil {
		return nil, err
	}
	network, err := readConfig(r, 0x55)
	if err != nil {
		return nil, err
	}

	c.SetDeadline(time.Time{})
	ok = true
	return &Session{Conn: c, Reader: r, Common: common, Network: network, Cookie: cookie, Gateway: g}, nil
}

// Logout invalidates the web session.
func (g Gateway) Logout(cookie string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := g.request(ctx, "/prx/000/http/localhost/logout", cookie, nil)
	return err
}

// KeepaliveInterval returns the gateway-requested heartbeat period.
func (s *Session) KeepaliveInterval(def time.Duration) time.Duration {
	raw, ok := s.Common["keepalive_interval"]
	if !ok {
		return def
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil || n <= 0 {
		return def
	}
	return time.Duration(n) * time.Second
}

// ClientIPv4 returns the tunnel address assigned to this session.
func (s *Session) ClientIPv4() (string, error) {
	return IPv4(s.Network["client_ipv4"])
}

// DNSServers returns the IPv4 resolvers pushed by the gateway.
func (s *Session) DNSServers() ([]string, error) {
	var items []struct {
		IPv4 []json.RawMessage `json:"ipv4"`
	}
	if err := json.Unmarshal(s.Network["dns_servers"], &items); err != nil {
		return nil, err
	}
	var out []string
	for _, item := range items {
		for _, v := range item.IPv4 {
			s, err := IPv4(v)
			if err != nil {
				continue
			}
			if net.ParseIP(s).To4() != nil {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

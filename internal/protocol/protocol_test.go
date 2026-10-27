package protocol

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"testing"
	"time"
)

func TestATPControlPacket(t *testing.T) {
	p := ATP(5, 42)
	if len(p) != 20 {
		t.Fatalf("len = %d, want 20", len(p))
	}
	if p[0] != 0x45 || p[9] != 255 {
		t.Fatalf("header = % x, want 0x45 .. 255", p[:10])
	}
	if !IsControl(p) {
		t.Fatal("IsControl = false")
	}
	cmd, param := Command(p)
	if cmd != 5 || param != 42 {
		t.Fatalf("Command = (%d, %d), want (5, 42)", cmd, param)
	}
}

func TestIsControlRejectsData(t *testing.T) {
	p := make([]byte, 20)
	p[0] = 0x45
	p[9] = 6 // TCP
	if IsControl(p) {
		t.Fatal("TCP packet reported as control")
	}
	if IsControl([]byte{0x45}) {
		t.Fatal("short packet reported as control")
	}
}

func TestReadPacket(t *testing.T) {
	payload := []byte{0xde, 0xad, 0xbe, 0xef}
	pkt := make([]byte, 20+len(payload))
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:], uint16(len(pkt)))
	copy(pkt[20:], payload)

	got, err := ReadPacket(bytes.NewReader(pkt))
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if !bytes.Equal(got, pkt) {
		t.Fatalf("got % x, want % x", got, pkt)
	}
}

func TestReadPacketRejectsBadLength(t *testing.T) {
	cases := map[string][]byte{
		"not ipv4":      {0x65, 0, 0, 20},
		"short ihl":     {0x41, 0, 0, 10},
		"len below ihl": {0x45, 0, 0, 4},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadPacket(bytes.NewReader(p)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestReadPacketTruncated(t *testing.T) {
	pkt := make([]byte, 40)
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:], 40)
	if _, err := ReadPacket(bytes.NewReader(pkt[:25])); err != io.ErrUnexpectedEOF {
		t.Fatalf("err = %v, want ErrUnexpectedEOF", err)
	}
}

func TestIPv4DecodesStringAndUint32(t *testing.T) {
	got, err := IPv4(json.RawMessage(`"10.1.2.3"`))
	if err != nil || got != "10.1.2.3" {
		t.Fatalf("string form: %q, %v", got, err)
	}
	// 1.2.3.4 as little-endian uint32.
	got, err = IPv4(json.RawMessage(`67305985`))
	if err != nil || got != "1.2.3.4" {
		t.Fatalf("uint32 form: %q, %v", got, err)
	}
}

func TestConfigBlockRoundTrip(t *testing.T) {
	body := []byte(`{"keepalive_interval":20,"client_ipv4":"172.16.9.9"}`)

	// Build the config block the way the gateway sends it.
	var buf bytes.Buffer
	cfg := make([]byte, 16+len(body))
	cfg[0] = 0x51
	binary.LittleEndian.PutUint32(cfg[12:], uint32(len(body)))
	copy(cfg[16:], body)
	buf.Write(cfg)
	// Trailing NUL padding must be stripped by the reader.
	buf.Write([]byte{0, 0, 0})

	m, err := readConfig(bufio.NewReader(&buf), 0x51)
	if err != nil {
		t.Fatalf("readConfig: %v", err)
	}
	if string(m["client_ipv4"]) != `"172.16.9.9"` {
		t.Fatalf("client_ipv4 = %s", m["client_ipv4"])
	}
}

func TestReadConfigRejectsWrongKind(t *testing.T) {
	cfg := make([]byte, 16)
	cfg[0] = 0x55
	if _, err := readConfig(bufio.NewReader(bytes.NewReader(cfg)), 0x51); err == nil {
		t.Fatal("expected error for mismatched kind")
	}
}

func TestReadConfigRejectsHugePayload(t *testing.T) {
	cfg := make([]byte, 16)
	cfg[0] = 0x51
	binary.LittleEndian.PutUint32(cfg[12:], 8<<20)
	if _, err := readConfig(bufio.NewReader(bytes.NewReader(cfg)), 0x51); err == nil {
		t.Fatal("expected error for oversized config")
	}
}

func TestKeepaliveInterval(t *testing.T) {
	s := &Session{Common: map[string]json.RawMessage{"keepalive_interval": json.RawMessage(`20`)}}
	if got := s.KeepaliveInterval(30 * time.Second); got != 20*time.Second {
		t.Fatalf("got %v, want 20s", got)
	}
	empty := &Session{Common: map[string]json.RawMessage{}}
	if got := empty.KeepaliveInterval(30 * time.Second); got != 30*time.Second {
		t.Fatalf("default = %v, want 30s", got)
	}
	bad := &Session{Common: map[string]json.RawMessage{"keepalive_interval": json.RawMessage(`"x"`)}}
	if got := bad.KeepaliveInterval(30 * time.Second); got != 30*time.Second {
		t.Fatalf("invalid = %v, want default", got)
	}
}

func TestDNSServers(t *testing.T) {
	s := &Session{Network: map[string]json.RawMessage{
		"dns_servers": json.RawMessage(`[{"ipv4":["202.120.190.208",67305985]}]`),
	}}
	got, err := s.DNSServers()
	if err != nil {
		t.Fatalf("DNSServers: %v", err)
	}
	if len(got) != 2 || got[0] != "202.120.190.208" || got[1] != "1.2.3.4" {
		t.Fatalf("got %v", got)
	}
}

func TestWriteAllShortWrite(t *testing.T) {
	if err := WriteAll(zeroWriter{}, []byte("abc")); err != io.ErrShortWrite {
		t.Fatalf("err = %v, want ErrShortWrite", err)
	}
}

type zeroWriter struct{}

func (zeroWriter) Write(p []byte) (int, error) { return 0, nil }

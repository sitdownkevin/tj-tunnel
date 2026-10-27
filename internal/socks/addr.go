// Package socks implements a SOCKS5 server (RFC 1928) with optional
// username/password authentication (RFC 1929), supporting CONNECT and
// UDP ASSOCIATE.
package socks

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
)

// SOCKS5 constants.
const (
	Version = 5

	cmdConnect      = 0x01
	cmdUDPAssociate = 0x03

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04

	methodNoAuth   = 0x00
	methodUserPass = 0x02
	methodReject   = 0xFF

	// Reply codes (RFC 1928 §6).
	replySuccess          = 0x00
	replyGeneralFailure   = 0x01
	replyHostUnreachable  = 0x04
	replyCmdNotSupported  = 0x07
	replyAddrNotSupported = 0x08
)

// errNoAcceptableMethod is returned when the client offers no method we
// support. Health probes rely on it, so it is not logged as a failure.
var errNoAcceptableMethod = errors.New("no acceptable authentication method")

// request is a parsed SOCKS5 request.
type request struct {
	cmd  byte
	host string
	port uint16
}

// readRequest reads and parses a CONNECT or UDP ASSOCIATE request.
func readRequest(r io.Reader) (request, error) {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return request{}, err
	}
	if h[0] != Version {
		return request{}, fmt.Errorf("unexpected SOCKS version 0x%02x", h[0])
	}
	if h[1] != cmdConnect && h[1] != cmdUDPAssociate {
		return request{}, fmt.Errorf("unsupported command 0x%02x", h[1])
	}

	var host string
	switch h[3] {
	case atypIPv4:
		var v [4]byte
		if _, err := io.ReadFull(r, v[:]); err != nil {
			return request{}, err
		}
		host = net.IP(v[:]).String()
	case atypIPv6:
		var v [16]byte
		if _, err := io.ReadFull(r, v[:]); err != nil {
			return request{}, err
		}
		host = net.IP(v[:]).String()
	case atypDomain:
		var l [1]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return request{}, err
		}
		b := make([]byte, l[0])
		if _, err := io.ReadFull(r, b); err != nil {
			return request{}, err
		}
		host = string(b)
	default:
		return request{}, fmt.Errorf("unsupported address type 0x%02x", h[3])
	}

	var p [2]byte
	if _, err := io.ReadFull(r, p[:]); err != nil {
		return request{}, err
	}
	return request{cmd: h[1], host: host, port: binary.BigEndian.Uint16(p[:])}, nil
}

// writeReply sends a reply with an all-zero IPv4 bind address.
func writeReply(w io.Writer, code byte) bool {
	return writeReplyAddr(w, code, netip.AddrPortFrom(netip.IPv4Unspecified(), 0))
}

// writeReplyAddr sends a reply carrying ap as BND.ADDR:BND.PORT.
func writeReplyAddr(w io.Writer, code byte, ap netip.AddrPort) bool {
	out := append([]byte{Version, code, 0}, encodeAddr(ap)...)
	_, err := w.Write(out)
	return err == nil
}

// encodeAddr encodes ap as ATYP+ADDR+PORT.
func encodeAddr(ap netip.AddrPort) []byte {
	ip := ap.Addr().Unmap()
	if ip.Is4() {
		b := []byte{atypIPv4}
		b = append(b, ip.AsSlice()...)
		return binary.BigEndian.AppendUint16(b, ap.Port())
	}
	b := []byte{atypIPv6}
	b = append(b, ip.AsSlice()...)
	return binary.BigEndian.AppendUint16(b, ap.Port())
}

// parseAddr decodes ATYP+ADDR+PORT from a UDP request header and returns the
// number of bytes consumed.
func parseAddr(p []byte) (host string, port uint16, n int, err error) {
	if len(p) < 1 {
		return "", 0, 0, errors.New("truncated address")
	}
	switch p[0] {
	case atypIPv4:
		if len(p) < 7 {
			return "", 0, 0, errors.New("truncated IPv4 address")
		}
		return net.IP(p[1:5]).String(), binary.BigEndian.Uint16(p[5:7]), 7, nil
	case atypDomain:
		if len(p) < 2 {
			return "", 0, 0, errors.New("truncated domain address")
		}
		l := int(p[1])
		if len(p) < 2+l+2 {
			return "", 0, 0, errors.New("truncated domain address")
		}
		return string(p[2 : 2+l]), binary.BigEndian.Uint16(p[2+l : 2+l+2]), 2 + l + 2, nil
	case atypIPv6:
		if len(p) < 19 {
			return "", 0, 0, errors.New("truncated IPv6 address")
		}
		return net.IP(p[1:17]).String(), binary.BigEndian.Uint16(p[17:19]), 19, nil
	default:
		return "", 0, 0, fmt.Errorf("unsupported address type 0x%02x", p[0])
	}
}

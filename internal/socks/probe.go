package socks

import (
	"fmt"
	"io"
	"net"
	"time"
)

// Probe checks that a SOCKS5 server answers the greeting. It is used as a
// container health check, so it deliberately tolerates servers that require
// authentication: a "no acceptable method" answer still proves liveness.
func Probe(addr string, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return fmt.Errorf("connect %s: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	// Offer only "no authentication".
	if _, err := conn.Write([]byte{Version, 1, methodNoAuth}); err != nil {
		return err
	}
	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return err
	}
	if reply[0] != Version {
		return fmt.Errorf("unexpected reply: % x", reply)
	}
	return nil
}

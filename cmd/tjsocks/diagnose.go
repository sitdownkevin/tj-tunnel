package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/sitdownkevin/tjsocks/internal/config"
	"github.com/sitdownkevin/tjsocks/internal/protocol"
)

// diagnose walks the connection path step by step and reports where it breaks.
// It is meant to be run on the machine that hosts the proxy, inside the
// container: `docker compose run --rm tjsocks -diagnose`.
func diagnose(cfg *config.Config, out io.Writer) error {
	fmt.Fprintf(out, "tjsocks 诊断\n")
	fmt.Fprintf(out, "  网关域名 : %s\n", cfg.Gateway)
	if cfg.GatewayAddress != "" {
		fmt.Fprintf(out, "  网关地址 : %s（覆盖 DNS）\n", cfg.GatewayAddress)
	}
	creds := "未提供"
	if cfg.Username != "" && cfg.Password != "" {
		creds = cfg.Username
	}
	fmt.Fprintf(out, "  登录账号 : %s\n\n", creds)

	// 1. DNS
	if cfg.GatewayAddress == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		start := time.Now()
		ips, err := net.DefaultResolver.LookupHost(ctx, cfg.Gateway)
		cancel()
		if err != nil {
			fail(out, "DNS 解析 %s", cfg.Gateway)
			fmt.Fprintf(out, "     %v\n", err)
			fmt.Fprintf(out, "     → 在该服务器的 docker-compose.yml 里设置 TJ_GATEWAY_ADDRESS: \"202.120.189.34:443\"\n\n")
		} else {
			ok(out, "DNS 解析 %s → %v (%.0fms)", cfg.Gateway, ips, ms(start))
			fmt.Fprintln(out)
		}
	}

	// 2. TCP
	address := cfg.GatewayAddress
	if address == "" {
		address = net.JoinHostPort(cfg.Gateway, "443")
	}
	start := time.Now()
	raw, err := (&net.Dialer{Timeout: 15 * time.Second}).Dial("tcp", address)
	if err != nil {
		fail(out, "TCP 连接 %s", address)
		fmt.Fprintf(out, "     %v\n", err)
		fmt.Fprintf(out, "     → 该服务器无法访问校园 VPN 网关：检查出站防火墙，或确认服务器所在网络是否被网关限制\n")
		return errors.New("无法连接网关")
	}
	ok(out, "TCP 连接 %s (%.0fms)", address, ms(start))
	fmt.Fprintln(out)

	// 3. TLS
	start = time.Now()
	tc := tls.Client(raw, &tls.Config{
		ServerName: cfg.Gateway,
		MinVersion: tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		},
	})
	if err := tc.Handshake(); err != nil {
		raw.Close()
		fail(out, "TLS 握手")
		fmt.Fprintf(out, "     %v\n", err)
		fmt.Fprintf(out, "     → 中间设备可能在拦截或重置 443 端口的 TLS\n")
		return errors.New("TLS 握手失败")
	}
	state := tc.ConnectionState()
	ok(out, "TLS 握手 (%.0fms, %s, %s)", ms(start), tlsVersion(state.Version), tls.CipherSuiteName(state.CipherSuite))
	if len(state.PeerCertificates) > 0 {
		c := state.PeerCertificates[0]
		fmt.Fprintf(out, "     证书: CN=%s  颁发者=%s  到期=%s\n",
			c.Subject.CommonName, c.Issuer.CommonName, c.NotAfter.Format("2006-01-02"))
	}
	tc.Close()
	fmt.Fprintln(out)

	// 4. Login
	if cfg.Username == "" || cfg.Password == "" {
		skip(out, "登录测试", "未提供账号密码")
		return nil
	}
	start = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	gateway := protocol.Gateway{Host: cfg.Gateway, Address: cfg.GatewayAddress}
	sess, err := gateway.Login(ctx, protocol.Credentials{
		Username: cfg.Username,
		Password: cfg.Password,
		Method:   cfg.AuthMethod,
	})
	if err != nil {
		fail(out, "登录")
		fmt.Fprintf(out, "     %v\n", err)
		fmt.Fprintf(out, "     → 账号/密码错误，或认证方式不是 LDAP3\n")
		return errors.New("登录失败")
	}
	ok(out, "登录成功 (%.0fms)", ms(start))

	ip, _ := sess.ClientIPv4()
	dns, _ := sess.DNSServers()
	fmt.Fprintf(out, "     隧道 IP: %s\n", ip)
	fmt.Fprintf(out, "     校内 DNS: %v\n", dns)
	_ = sess.Conn.Close()
	_ = gateway.Logout(sess.Cookie)

	fmt.Fprintf(out, "\n全部检查通过 ✅ 若代理仍不可用，请确认端口映射同时包含 TCP 与 UDP。\n")
	return nil
}

func ok(out io.Writer, format string, args ...any) {
	fmt.Fprintf(out, "[ ✓ ] "+format+"\n", args...)
}

func fail(out io.Writer, format string, args ...any) {
	fmt.Fprintf(out, "[ ✗ ] "+format+"\n", args...)
}

func skip(out io.Writer, label, reason string) {
	fmt.Fprintf(out, "[ - ] %s（%s）\n", label, reason)
}

func ms(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

func tlsVersion(v uint16) string {
	switch v {
	case tls.VersionTLS12:
		return "TLS1.2"
	case tls.VersionTLS13:
		return "TLS1.3"
	}
	return fmt.Sprintf("TLS 0x%04x", v)
}

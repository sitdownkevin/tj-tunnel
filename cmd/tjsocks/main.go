// Command tjsocks exposes a Tongji campus VPN tunnel as a SOCKS5 proxy that
// supports both TCP (CONNECT) and UDP (UDP ASSOCIATE) traffic.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/your-github-user/tjsocks/internal/config"
	"github.com/your-github-user/tjsocks/internal/netstack"
	"github.com/your-github-user/tjsocks/internal/protocol"
	"github.com/your-github-user/tjsocks/internal/resolver"
	"github.com/your-github-user/tjsocks/internal/socks"
	"github.com/your-github-user/tjsocks/internal/tunnel"
	"github.com/your-github-user/tjsocks/internal/version"
)

const (
	reconnectMaxDelay = 60 * time.Second
	stableSession     = time.Minute
)

func main() {
	switch first := firstArg(); first {
	case "-version", "--version":
		fmt.Println("tjsocks " + version.String())
		return
	case "-healthcheck", "--healthcheck":
		addr := "127.0.0.1:1080"
		if len(os.Args) > 2 {
			addr = os.Args[2]
		}
		if err := socks.Probe(addr, 3*time.Second); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	case "-help", "--help", "-h":
		usage()
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "tjsocks: "+err.Error())
		os.Exit(1)
	}
}

func firstArg() string {
	if len(os.Args) < 2 {
		return ""
	}
	return os.Args[1]
}

func usage() {
	fmt.Fprint(os.Stderr, `tjsocks - Tongji campus VPN to SOCKS5 gateway (TCP + UDP)

Usage:
  tjsocks [flags]
  tjsocks -healthcheck [host:port]   probe a running instance
  tjsocks -version                   print build information

Flags:
  -u, -p                 campus VPN username / password
  -listen                SOCKS5 TCP listen address (default ":1080")
  -udp-listen            UDP relay listen address (defaults to -listen port)
  -udp-advertise         UDP relay address handed to clients
  -socks-user/-socks-pass SOCKS5 authentication (optional)
  -config                JSON configuration file
  -log-level             debug|info|warn|error (default "info")

Environment variables: TJ_USER, TJ_PASSWORD, TJ_AUTH_METHOD, TJ_GATEWAY,
TJ_GATEWAY_ADDRESS, TJ_LOG_LEVEL, SOCKS_LISTEN, SOCKS_UDP_LISTEN,
SOCKS_UDP_ADVERTISE, SOCKS_USER, SOCKS_PASS.
`)
}

func run() error {
	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if err != nil {
		return err
	}
	cfg.Username, cfg.Password = promptCredentials(cfg.Username, cfg.Password)
	if err := cfg.Validate(); err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)
	logger.Info("starting", "version", version.Version, "config", cfg.Describe())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	defer listener.Close()
	logger.Info("SOCKS5 listening, waiting for the VPN tunnel", "addr", cfg.Listen)

	return serve(ctx, cfg, listener, logger)
}

// serve runs the login/tunnel/serve loop, reconnecting with backoff.
func serve(ctx context.Context, cfg *config.Config, listener net.Listener, logger *slog.Logger) error {
	var attempt int
	for ctx.Err() == nil {
		if attempt > 0 {
			delay := min(reconnectMaxDelay, time.Duration(1)<<min(attempt, 6)*time.Second)
			logger.Warn("reconnecting", "in", delay.String(), "attempt", attempt)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(delay):
			}
		}
		attempt++

		started := time.Now()
		err := session(ctx, cfg, listener, logger)
		if ctx.Err() != nil {
			return nil
		}
		logger.Warn("tunnel closed", "err", err, "uptime", time.Since(started).Round(time.Second))
		if time.Since(started) > stableSession {
			attempt = 0
		}
	}
	return nil
}

// session logs in, serves until the tunnel dies, then cleans up.
func session(ctx context.Context, cfg *config.Config, listener net.Listener, logger *slog.Logger) error {
	gateway := protocol.Gateway{
		Host:    cfg.Gateway,
		Address: cfg.GatewayAddress,
	}
	logger.Info("logging in", "gateway", cfg.Gateway, "user", cfg.Username)
	sess, err := gateway.Login(ctx, protocol.Credentials{
		Username: cfg.Username,
		Password: cfg.Password,
		Method:   cfg.AuthMethod,
	})
	if err != nil {
		return err
	}
	logger.Info("authenticated")

	tun, err := tunnel.Start(ctx, sess)
	if err != nil {
		_ = sess.Conn.Close()
		return err
	}

	pushed, err := sess.DNSServers()
	if err != nil {
		_ = sess.Conn.Close()
		return fmt.Errorf("read gateway DNS configuration: %w", err)
	}
	dnsServers := usableDNS(pushed)
	if len(dnsServers) == 0 {
		_ = sess.Conn.Close()
		return errors.New("gateway pushed no usable IPv4 DNS server")
	}
	res, err := resolver.New(tun.Stack, dnsServers)
	if err != nil {
		_ = sess.Conn.Close()
		return err
	}
	logger.Info("tunnel ready", "tunnel_ip", tun.LocalIP, "dns", dnsServers)

	counters := &socks.Counters{}
	network := &netstack.Network{Stack: tun.Stack}
	server := &socks.Server{
		Listener: listener,
		Resolver: res,
		Dialer:   network,
		Log:      logger,
		Counters: counters,
	}
	if cfg.AuthEnabled() {
		server.Auth = &socks.Auth{Username: cfg.SocksUser, Password: cfg.SocksPass}
	}
	if relay, err := socks.NewUDPRelay(cfg.UDPListen, cfg.UDPAdvertise, res, network, logger, counters); err != nil {
		logger.Warn("UDP relay disabled, serving TCP only", "err", err)
	} else {
		defer relay.Close()
		server.UDP = relay
		logger.Info("SOCKS5 ready",
			"tcp", cfg.Listen, "udp", cfg.UDPListen, "advertise", cfg.UDPAdvertise)
	}

	serveCtx, stopServe := context.WithCancel(ctx)
	defer stopServe()
	go server.Serve(serveCtx)

	err = <-tun.Done
	stopServe()
	tun.Logout()
	return err
}

// usableDNS keeps the IPv4 entries of the gateway's DNS list.
func usableDNS(hosts []string) []netip.Addr {
	out := make([]netip.Addr, 0, len(hosts))
	for _, h := range hosts {
		if addr, err := netip.ParseAddr(h); err == nil && addr.Is4() {
			out = append(out, addr)
		}
	}
	return out
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

// promptCredentials asks for missing credentials when stdin is a terminal.
func promptCredentials(user, pass string) (string, string) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return user, pass
	}
	if user == "" {
		fmt.Fprint(os.Stderr, "campus VPN username: ")
		fmt.Scanln(&user)
		user = strings.TrimSpace(user)
	}
	if pass == "" {
		fmt.Fprint(os.Stderr, "campus VPN password: ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err == nil {
			pass = string(b)
		}
	}
	return user, pass
}

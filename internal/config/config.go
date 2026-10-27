// Package config loads and validates runtime settings from flags,
// environment variables and an optional JSON file.
package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
)

// Config is the resolved configuration.
type Config struct {
	// Campus VPN credentials.
	Username   string
	Password   string
	AuthMethod string

	// Gateway location.
	Gateway        string
	GatewayAddress string

	// SOCKS5 listeners.
	Listen       string
	UDPListen    string
	UDPAdvertise string

	// Optional proxy authentication.
	SocksUser string
	SocksPass string

	// LogLevel is one of debug, info, warn, error.
	LogLevel string

	// Web dashboard.
	WebListen string // empty disables it
	WebUser   string // empty reuses the SOCKS credentials
	WebPass   string
}

// file mirrors Config for JSON (de)serialisation.
type file struct {
	Username       string `json:"username"`
	Password       string `json:"password"`
	AuthMethod     string `json:"auth_method"`
	Gateway        string `json:"gateway"`
	GatewayAddress string `json:"gateway_address"`
	Listen         string `json:"listen"`
	UDPListen      string `json:"udp_listen"`
	UDPAdvertise   string `json:"udp_advertise"`
	SocksUser      string `json:"socks_user"`
	SocksPass      string `json:"socks_pass"`
	LogLevel       string `json:"log_level"`
	WebListen      string `json:"web_listen"`
	WebUser        string `json:"web_user"`
	WebPass        string `json:"web_pass"`
}

func (c *Config) apply(f *file) {
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&c.Username, f.Username)
	set(&c.Password, f.Password)
	set(&c.AuthMethod, f.AuthMethod)
	set(&c.Gateway, f.Gateway)
	set(&c.GatewayAddress, f.GatewayAddress)
	set(&c.Listen, f.Listen)
	set(&c.UDPListen, f.UDPListen)
	set(&c.UDPAdvertise, f.UDPAdvertise)
	set(&c.SocksUser, f.SocksUser)
	set(&c.SocksPass, f.SocksPass)
	set(&c.LogLevel, f.LogLevel)
	set(&c.WebListen, f.WebListen)
	set(&c.WebUser, f.WebUser)
	set(&c.WebPass, f.WebPass)
}

// Load resolves configuration with the following precedence (low to high):
// defaults, ~/.tjsocks.json, -config file, environment variables, flags.
//
// getenv is injected for testability; pass os.Getenv in production.
func Load(args []string, getenv func(string) string) (*Config, error) {
	cfg := &Config{Gateway: "vpn.tongji.cn", Listen: ":1080", LogLevel: "info"}

	fs := flag.NewFlagSet("tjsocks", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		configPath = fs.String("config", "", "path to a JSON configuration file")
		user       = fs.String("u", "", "campus VPN username")
		pass       = fs.String("p", "", "campus VPN password")
		listen     = fs.String("listen", "", "SOCKS5 TCP listen address, e.g. 0.0.0.0:1080")
		udpListen  = fs.String("udp-listen", "", "SOCKS5 UDP relay listen address (defaults to the -listen port)")
		udpAdv     = fs.String("udp-advertise", "", "UDP relay address handed to clients (for NAT/public deployments)")
		socksUser  = fs.String("socks-user", "", "SOCKS5 authentication username (optional)")
		socksPass  = fs.String("socks-pass", "", "SOCKS5 authentication password (optional)")
		logLevel   = fs.String("log-level", "", "log level: debug, info, warn or error")
		webListen  = fs.String("web-listen", "", "web dashboard listen address, e.g. 0.0.0.0:8080 (empty disables it)")
		webUser    = fs.String("web-user", "", "dashboard basic auth username (defaults to the SOCKS user)")
		webPass    = fs.String("web-pass", "", "dashboard basic auth password (defaults to the SOCKS password)")
	)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	// ~/.tjsocks.json, unless -config was given.
	if *configPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			if err := cfg.loadFile(filepath.Join(home, ".tjsocks.json"), false); err != nil {
				return nil, err
			}
		}
	} else if err := cfg.loadFile(*configPath, true); err != nil {
		return nil, err
	}

	// Environment.
	env := func(dst *string, name string) {
		if v := getenv(name); v != "" {
			*dst = v
		}
	}
	env(&cfg.Username, "TJ_USER")
	env(&cfg.Password, "TJ_PASSWORD")
	env(&cfg.AuthMethod, "TJ_AUTH_METHOD")
	env(&cfg.Gateway, "TJ_GATEWAY")
	env(&cfg.GatewayAddress, "TJ_GATEWAY_ADDRESS")
	env(&cfg.Listen, "SOCKS_LISTEN")
	env(&cfg.UDPListen, "SOCKS_UDP_LISTEN")
	env(&cfg.UDPAdvertise, "SOCKS_UDP_ADVERTISE")
	env(&cfg.SocksUser, "SOCKS_USER")
	env(&cfg.SocksPass, "SOCKS_PASS")
	env(&cfg.LogLevel, "TJ_LOG_LEVEL")
	env(&cfg.LogLevel, "LOG_LEVEL")
	env(&cfg.WebListen, "WEB_LISTEN")
	env(&cfg.WebUser, "WEB_USER")
	env(&cfg.WebPass, "WEB_PASS")

	// Flags win over everything.
	for _, f := range []struct {
		dst *string
		val string
	}{
		{&cfg.Username, *user},
		{&cfg.Password, *pass},
		{&cfg.Listen, *listen},
		{&cfg.UDPListen, *udpListen},
		{&cfg.UDPAdvertise, *udpAdv},
		{&cfg.SocksUser, *socksUser},
		{&cfg.SocksPass, *socksPass},
		{&cfg.LogLevel, *logLevel},
		{&cfg.WebListen, *webListen},
		{&cfg.WebUser, *webUser},
		{&cfg.WebPass, *webPass},
	} {
		if f.val != "" {
			*f.dst = f.val
		}
	}

	cfg.derive()
	return cfg, nil
}

func (c *Config) loadFile(path string, required bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !required {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	c.apply(&f)
	return nil
}

// derive fills in values that depend on other settings.
func (c *Config) derive() {
	if c.AuthMethod == "" {
		c.AuthMethod = "LDAP3"
	}
	if c.UDPListen == "" {
		c.UDPListen = ":" + Port(c.Listen)
	}
	if c.UDPAdvertise == "" {
		// 0.0.0.0 tells clients to reuse the address they already reached;
		// Clash, mihomo and sing-box do this automatically.
		c.UDPAdvertise = "0.0.0.0:" + Port(c.UDPListen)
	}
}

// Validate reports configuration problems.
func (c *Config) Validate() error {
	if c.Username == "" {
		return errors.New("campus VPN username is required (TJ_USER, -u or ~/.tjsocks.json)")
	}
	if c.Password == "" {
		return errors.New("campus VPN password is required (TJ_PASSWORD, -p or ~/.tjsocks.json)")
	}
	if c.Gateway == "" {
		return errors.New("gateway host is required")
	}
	for _, f := range []struct {
		name, val string
	}{{"listen", c.Listen}, {"udp_listen", c.UDPListen}, {"udp_advertise", c.UDPAdvertise}} {
		if _, _, err := net.SplitHostPort(f.val); err != nil {
			return fmt.Errorf("%s %q is not a host:port address: %w", f.name, f.val, err)
		}
	}
	if (c.SocksUser == "") != (c.SocksPass == "") {
		return errors.New("socks_user and socks_pass must be set together")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log_level %q must be one of debug, info, warn, error", c.LogLevel)
	}
	if c.WebListen != "" {
		if _, _, err := net.SplitHostPort(c.WebListen); err != nil {
			return fmt.Errorf("web_listen %q is not a host:port address: %w", c.WebListen, err)
		}
		if !c.AuthEnabled() && c.WebUser != "" && c.WebPass == "" {
			return errors.New("web_user requires web_pass")
		}
	}
	return nil
}

// DashboardAuth returns the credentials the dashboard should require. It
// defaults to the SOCKS credentials so a public deployment is never left wide
// open just because the dashboard was enabled.
func (c *Config) DashboardAuth() (string, string) {
	if c.WebUser != "" {
		return c.WebUser, c.WebPass
	}
	return c.SocksUser, c.SocksPass
}

// AuthEnabled reports whether SOCKS5 authentication is configured.
func (c *Config) AuthEnabled() bool { return c.SocksUser != "" }

// Port extracts the port from a host:port string, defaulting to 1080.
func Port(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil && p != "" {
		return p
	}
	if _, p, err := net.SplitHostPort(":" + addr); err == nil && p != "" {
		return p
	}
	return "1080"
}

// Describe renders the effective settings without secrets, for logging.
func (c *Config) Describe() string {
	auth := "off"
	if c.AuthEnabled() {
		auth = "on"
	}
	gateway := c.Gateway
	if c.GatewayAddress != "" {
		gateway += " via " + c.GatewayAddress
	}
	web := "off"
	if c.WebListen != "" {
		web = c.WebListen
	}
	return fmt.Sprintf("gateway=%s listen=%s udp=%s advertise=%s auth=%s web=%s user=%s",
		gateway, c.Listen, c.UDPListen, c.UDPAdvertise, auth, web, c.Username)
}

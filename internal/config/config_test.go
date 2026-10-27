package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func noEnv(string) string { return "" }

// loadValid loads the configuration and applies validation.
func loadValid(args []string, getenv func(string) string) (*Config, error) {
	c, err := Load(args, getenv)
	if err != nil {
		return nil, err
	}
	return c, c.Validate()
}

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := loadValid([]string{"-u", "user", "-p", "pass"}, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Gateway != "vpn.tongji.cn" {
		t.Errorf("gateway = %q", c.Gateway)
	}
	if c.Listen != ":1080" {
		t.Errorf("listen = %q", c.Listen)
	}
	if c.UDPListen != ":1080" {
		t.Errorf("udp listen = %q", c.UDPListen)
	}
	if c.UDPAdvertise != "0.0.0.0:1080" {
		t.Errorf("udp advertise = %q", c.UDPAdvertise)
	}
	if c.AuthEnabled() {
		t.Error("auth should be disabled by default")
	}
}

func TestUDPDefaultsFollowListenPort(t *testing.T) {
	c, err := loadValid([]string{"-u", "u", "-p", "p", "-listen", "0.0.0.0:7777"}, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.UDPListen != ":7777" {
		t.Errorf("udp listen = %q, want :7777", c.UDPListen)
	}
	if c.UDPAdvertise != "0.0.0.0:7777" {
		t.Errorf("udp advertise = %q", c.UDPAdvertise)
	}
}

func TestEnvironmentOverridesDefaults(t *testing.T) {
	env := envFrom(map[string]string{
		"TJ_USER":            "envuser",
		"TJ_PASSWORD":        "envpass",
		"SOCKS_LISTEN":       "127.0.0.1:9999",
		"SOCKS_USER":         "proxy",
		"SOCKS_PASS":         "secret",
		"TJ_GATEWAY_ADDRESS": "203.0.113.5:443",
	})
	c, err := loadValid(nil, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Username != "envuser" || c.Password != "envpass" {
		t.Errorf("credentials = %q/%q", c.Username, c.Password)
	}
	if c.Listen != "127.0.0.1:9999" {
		t.Errorf("listen = %q", c.Listen)
	}
	if c.GatewayAddress != "203.0.113.5:443" {
		t.Errorf("gateway address = %q", c.GatewayAddress)
	}
	if !c.AuthEnabled() {
		t.Error("auth should be enabled")
	}
}

func TestFlagsOverrideEnvironment(t *testing.T) {
	env := envFrom(map[string]string{"TJ_USER": "envuser", "TJ_PASSWORD": "envpass", "SOCKS_LISTEN": ":1111"})
	c, err := loadValid([]string{"-listen", ":2222"}, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Listen != ":2222" {
		t.Errorf("listen = %q, want :2222", c.Listen)
	}
	if c.Username != "envuser" {
		t.Errorf("username = %q, want envuser", c.Username)
	}
}

func TestConfigFileIsUsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")
	body := `{"username":"fileuser","password":"filepass","listen":"127.0.0.1:1234","socks_user":"a","socks_pass":"b"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadValid([]string{"-config", path}, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Username != "fileuser" || c.Password != "filepass" {
		t.Errorf("credentials = %q/%q", c.Username, c.Password)
	}
	if c.Listen != "127.0.0.1:1234" {
		t.Errorf("listen = %q", c.Listen)
	}
}

func TestMissingCredentialsFail(t *testing.T) {
	_, err := loadValid(nil, noEnv)
	if err == nil {
		t.Fatal("expected an error when credentials are missing")
	}
	if !strings.Contains(err.Error(), "username") {
		t.Errorf("error = %v", err)
	}
}

func TestHalfConfiguredAuthFails(t *testing.T) {
	_, err := loadValid([]string{"-u", "u", "-p", "p", "-socks-user", "only-user"}, noEnv)
	if err == nil {
		t.Fatal("expected an error for a half-configured proxy auth")
	}
}

func TestInvalidListenAddressFails(t *testing.T) {
	for _, addr := range []string{"not-an-address", "1.2.3.4"} {
		if _, err := loadValid([]string{"-u", "u", "-p", "p", "-listen", addr}, noEnv); err == nil {
			t.Errorf("expected an error for %q", addr)
		}
	}
}

func TestUnreadableConfigFileFails(t *testing.T) {
	_, err := Load([]string{"-config", filepath.Join(t.TempDir(), "missing.json")}, noEnv)
	if err == nil {
		t.Fatal("expected an error for a missing -config file")
	}
}

func TestDescribeHidesPassword(t *testing.T) {
	c, err := loadValid([]string{"-u", "u", "-p", "topsecret", "-socks-user", "x", "-socks-pass", "y"}, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d := c.Describe()
	if strings.Contains(d, "topsecret") || strings.Contains(d, "y") && strings.Contains(d, "pass") {
		t.Errorf("Describe leaked secrets: %s", d)
	}
}

func TestPort(t *testing.T) {
	cases := map[string]string{
		":1080":          "1080",
		"0.0.0.0:8080":   "8080",
		"[::1]:9050":     "9050",
		"127.0.0.1:1234": "1234",
	}
	for in, want := range cases {
		if got := Port(in); got != want {
			t.Errorf("Port(%q) = %q, want %q", in, got, want)
		}
	}
}

package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/sitdownkevin/tj-tunnel/internal/logbus"
	"github.com/sitdownkevin/tj-tunnel/internal/status"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func newTestServer(t *testing.T, opts Options) (*httptest.Server, *status.State) {
	t.Helper()
	if opts.Log == nil {
		opts.Log = discardLogger()
	}
	if opts.State == nil {
		opts.State = status.New("test-version", nil)
	}
	s := New(opts)
	srv := httptest.NewServer(s.http.Handler)
	t.Cleanup(srv.Close)
	return srv, opts.State
}

// client returns an HTTP client that keeps cookies, like a browser.
func client(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, Timeout: 5 * time.Second}
}

func doLogin(t *testing.T, c *http.Client, base, user, pass string) *http.Response {
	t.Helper()
	body := strings.NewReader(`{"username":"` + user + `","password":"` + pass + `"}`)
	res, err := c.Post(base+"/api/login", "application/json", body)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestServesEmbeddedBundle(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(strings.ToLower(string(body)), "<!doctype html") {
		t.Fatalf("unexpected bundle: %s", body)
	}
	if res.Header.Get("Content-Security-Policy") == "" {
		t.Error("missing CSP header")
	}
}

func TestSPAFallbackAndAssetCaching(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	res, err := http.Get(srv.URL + "/some/client/route")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("SPA route status = %d", res.StatusCode)
	}

	// Assets carry the immutable cache header. Skip when this checkout only
	// has the placeholder page (no bundle built yet).
	page, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	html, _ := io.ReadAll(page.Body)
	page.Body.Close()

	asset := firstAssetPath(t, string(html))
	if asset == "" {
		t.Log("no bundle asset referenced; skipping cache header assertion")
		return
	}
	assets, err := http.Get(srv.URL + asset)
	if err != nil {
		t.Fatalf("GET %s: %v", asset, err)
	}
	defer assets.Body.Close()
	if assets.StatusCode != http.StatusOK {
		t.Fatalf("%s status = %d", asset, assets.StatusCode)
	}
	if cc := assets.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("asset cache-control = %q", cc)
	}
}

func TestLoginFlow(t *testing.T) {
	srv, _ := newTestServer(t, Options{Username: "admin", Password: "secret"})
	c := client(t)

	if res := doLogin(t, c, srv.URL, "admin", "wrong"); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d", res.StatusCode)
	}

	res := doLogin(t, c, srv.URL, "admin", "secret")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", res.StatusCode)
	}
	if res.Header.Get("Set-Cookie") == "" {
		t.Fatal("no session cookie issued")
	}

	// /api/session reflects the cookie, and /api/status becomes readable.
	var sessionBody map[string]bool
	readJSON(t, c, srv.URL+"/api/session", &sessionBody)
	if !sessionBody["authenticated"] {
		t.Fatal("session not authenticated after login")
	}
	if res, err := c.Get(srv.URL + "/api/status"); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("status endpoint: %v / %v", err, res)
	} else {
		res.Body.Close()
	}

	// Logout invalidates it.
	if _, err := c.Post(srv.URL+"/api/logout", "application/json", nil); err != nil {
		t.Fatalf("logout: %v", err)
	}
	readJSON(t, c, srv.URL+"/api/session", &sessionBody)
	if sessionBody["authenticated"] {
		t.Fatal("session still authenticated after logout")
	}
}

// firstAssetPath extracts the first /assets/... reference from the page.
func firstAssetPath(t *testing.T, html string) string {
	t.Helper()
	const marker = "/assets/"
	idx := strings.Index(html, marker)
	if idx < 0 {
		return ""
	}
	rest := html[idx:]
	end := strings.IndexAny(rest, `"'>`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func readJSON(t *testing.T, c *http.Client, url string, dst any) {
	t.Helper()
	res, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(dst); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}

func TestStatusRequiresSession(t *testing.T) {
	srv, _ := newTestServer(t, Options{Username: "admin", Password: "secret"})
	res, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
}

func TestWebSocketPushesSnapshots(t *testing.T) {
	srv, st := newTestServer(t, Options{Username: "admin", Password: "secret"})
	c := client(t)
	doLogin(t, c, srv.URL, "admin", "secret")
	st.SetReady("10.5.5.5", []string{"1.1.1.1"}, "0.0.0.0:1080")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/ws", &websocket.DialOptions{
		HTTPClient: c,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	kind, data := readFrame(t, ctx, conn)
	if kind != "status" {
		t.Fatalf("frame type = %q, want status", kind)
	}
	var got status.Snapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TunnelIP != "10.5.5.5" || got.Version != "test-version" {
		t.Fatalf("snapshot = %+v", got)
	}

	// A state change must reach the same connection.
	st.SetPhase(status.PhaseReconnecting)
	for i := 0; i < 5; i++ {
		kind, data = readFrame(t, ctx, conn)
		if kind != "status" {
			continue
		}
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Phase == status.PhaseReconnecting {
			return
		}
	}
	t.Fatal("state change never arrived")
}

// readFrame reads one multiplexed frame from the socket.
func readFrame(t *testing.T, ctx context.Context, conn *websocket.Conn) (string, json.RawMessage) {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var frame struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	return frame.Type, frame.Data
}

func TestWebSocketStreamsLogs(t *testing.T) {
	bus := logbus.New(50)
	bus.Add(logbus.Entry{Time: time.Now(), Level: "INFO", Message: "history line"})

	srv, _ := newTestServer(t, Options{Username: "admin", Password: "secret", Logs: bus})
	c := client(t)
	doLogin(t, c, srv.URL, "admin", "secret")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/ws", &websocket.DialOptions{HTTPClient: c})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	// The buffered history is replayed first.
	kind, data := readFrame(t, ctx, conn)
	if kind != "logs" {
		t.Fatalf("first frame type = %q, want logs", kind)
	}
	var history []logbus.Entry
	if err := json.Unmarshal(data, &history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history) != 1 || history[0].Message != "history line" {
		t.Fatalf("history = %+v", history)
	}

	// Live records arrive without waiting for a status tick.
	bus.Add(logbus.Entry{Time: time.Now(), Level: "WARN", Message: "live line"})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		kind, data = readFrame(t, ctx, conn)
		if kind != "log" {
			continue
		}
		var entry logbus.Entry
		if err := json.Unmarshal(data, &entry); err != nil {
			t.Fatalf("decode entry: %v", err)
		}
		if entry.Message == "live line" && entry.Level == "WARN" {
			return
		}
	}
	t.Fatal("live log entry never arrived")
}

func TestLogHistoryEndpoint(t *testing.T) {
	bus := logbus.New(50)
	for _, msg := range []string{"one", "two", "three"} {
		bus.Add(logbus.Entry{Time: time.Now(), Level: "INFO", Message: msg})
	}
	srv, _ := newTestServer(t, Options{Username: "admin", Password: "secret", Logs: bus})
	c := client(t)

	if res, err := c.Get(srv.URL + "/api/logs"); err != nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /api/logs: %v / %v", err, res)
	}

	doLogin(t, c, srv.URL, "admin", "secret")
	res, err := c.Get(srv.URL + "/api/logs?limit=2")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	var entries []logbus.Entry
	if err := json.NewDecoder(res.Body).Decode(&entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 2 || entries[0].Message != "two" || entries[1].Message != "three" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestWebSocketRequiresSession(t *testing.T) {
	srv, _ := newTestServer(t, Options{Username: "admin", Password: "secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, res, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/ws", nil)
	if err == nil {
		t.Fatal("unauthenticated websocket should be rejected")
	}
	if res != nil && res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
}

func TestWebSocketRejectsCrossOrigin(t *testing.T) {
	srv, _ := newTestServer(t, Options{Username: "admin", Password: "secret"})
	c := client(t)
	doLogin(t, c, srv.URL, "admin", "secret")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, res, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/ws", &websocket.DialOptions{
		HTTPClient: c,
		HTTPHeader: http.Header{"Origin": []string{"http://evil.example"}},
	})
	if err == nil {
		t.Fatal("cross-origin websocket should be rejected")
	}
	if res != nil && res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.StatusCode)
	}
}

func TestTokenRejectsTampering(t *testing.T) {
	s := New(Options{Username: "u", Password: "p", Log: discardLogger()})
	token := s.issueToken(time.Now().Add(time.Hour))
	if !s.tokenValid(token) {
		t.Fatal("fresh token rejected")
	}
	if s.tokenValid(token + "x") {
		t.Fatal("tampered signature accepted")
	}
	expired := s.issueToken(time.Now().Add(-time.Minute))
	if s.tokenValid(expired) {
		t.Fatal("expired token accepted")
	}
	other := New(Options{Username: "u", Password: "p", Log: discardLogger()})
	if other.tokenValid(token) {
		t.Fatal("token from another instance accepted")
	}
}

func TestOpenDashboardWhenNoCredentials(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	var body map[string]bool
	readJSON(t, http.DefaultClient, srv.URL+"/api/session", &body)
	if !body["authenticated"] {
		t.Fatal("dashboard without credentials should allow access")
	}
}

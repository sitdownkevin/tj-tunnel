// Package web serves the dashboard: an embedded React/MUI bundle, a small
// session-based login API, and a WebSocket that pushes status snapshots.
package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/sitdownkevin/tj-tunnel/internal/status"
)

//go:embed all:dist
var dist embed.FS

const (
	pushInterval     = time.Second
	sessionCookie    = "tjt_session"
	sessionLifetime  = 12 * time.Hour
	maxLoginBodySize = 4 << 10
	writeWait        = 10 * time.Second
)

// Controller drives the tunnel lifecycle from the dashboard.
type Controller interface {
	Start() error
	Stop()
	Running() bool
	AutoReconnect() bool
	SetAutoReconnect(bool)
}

// Options configures the dashboard.
type Options struct {
	Addr       string // listen address, empty disables the dashboard
	Username   string // login required when set; empty leaves the dashboard open
	Password   string
	Log        *slog.Logger
	State      *status.State
	Controller Controller // nil disables the control endpoints
}

// Server is the dashboard HTTP server.
type Server struct {
	opts   Options
	http   *http.Server
	secret []byte // HMAC key for session cookies, rotated on every start
	log    *slog.Logger
}

// New builds the dashboard server.
func New(opts Options) *Server {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic("web: cannot seed session secret: " + err.Error())
	}
	s := &Server{opts: opts, secret: secret, log: opts.Log}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("GET /api/ws", s.handleWS)
	mux.HandleFunc("GET /api/tunnel", s.handleTunnelState)
	mux.HandleFunc("PUT /api/tunnel", s.handleTunnelControl)
	mux.HandleFunc("GET /api/status", s.handleStatus) // plain snapshot, handy for scripts
	mux.Handle("GET /", s.staticHandler())

	s.http = &http.Server{
		Addr:              opts.Addr,
		Handler:           s.withSecurityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // the WebSocket stream must not be cut
		IdleTimeout:       120 * time.Second,
	}
	return s
}

// ListenAndServe starts the dashboard and returns when it stops.
func (s *Server) ListenAndServe() error {
	if s.opts.Addr == "" {
		return nil
	}
	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return fmt.Errorf("dashboard listen on %s: %w", s.opts.Addr, err)
	}
	s.log.Info("dashboard ready", "url", "http://"+ln.Addr().String(), "auth", s.opts.Username != "")
	if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown stops the dashboard.
func (s *Server) Shutdown() { _ = s.http.Close() }

func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
				"script-src 'self'; connect-src 'self' ws: wss:; base-uri 'none'; form-action 'none'")
		next.ServeHTTP(w, r)
	})
}

// ---- authentication -------------------------------------------------------

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.opts.Username == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxLoginBodySize))
	if err != nil {
		writeError(w, http.StatusBadRequest, "无法读取请求")
		return
	}
	var req loginRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if !s.validCredentials(req.Username, req.Password) {
		// Slow down brute-force attempts a little.
		time.Sleep(300 * time.Millisecond)
		writeError(w, http.StatusUnauthorized, "账号或密码错误")
		return
	}

	token := s.issueToken(time.Now().Add(sessionLifetime))
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionLifetime.Seconds()),
		Secure:   r.TLS != nil,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": s.authorized(r)})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "未登录")
		return
	}
	writeJSON(w, http.StatusOK, s.opts.State.Snapshot())
}

type tunnelRequest struct {
	Running       *bool `json:"running"`
	AutoReconnect *bool `json:"auto_reconnect"`
}

func (s *Server) handleTunnelState(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "未登录")
		return
	}
	if s.opts.Controller == nil {
		writeError(w, http.StatusServiceUnavailable, "控制接口不可用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{
		"running":        s.opts.Controller.Running(),
		"auto_reconnect": s.opts.Controller.AutoReconnect(),
	})
}

// handleTunnelControl starts or stops the tunnel and toggles auto-reconnect.
func (s *Server) handleTunnelControl(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "未登录")
		return
	}
	if s.opts.Controller == nil {
		writeError(w, http.StatusServiceUnavailable, "控制接口不可用")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxLoginBodySize))
	if err != nil {
		writeError(w, http.StatusBadRequest, "无法读取请求")
		return
	}
	var req tunnelRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}

	if req.AutoReconnect != nil {
		s.opts.Controller.SetAutoReconnect(*req.AutoReconnect)
	}
	if req.Running != nil {
		if *req.Running {
			if err := s.opts.Controller.Start(); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		} else {
			s.opts.Controller.Stop()
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{
		"running":        s.opts.Controller.Running(),
		"auto_reconnect": s.opts.Controller.AutoReconnect(),
	})
}

func (s *Server) validCredentials(user, pass string) bool {
	return subtle.ConstantTimeCompare([]byte(user), []byte(s.opts.Username)) == 1 &&
		subtle.ConstantTimeCompare([]byte(pass), []byte(s.opts.Password)) == 1
}

// issueToken returns "<expiry>.<hmac>" so sessions survive without server state.
func (s *Server) issueToken(expiry time.Time) string {
	payload := strconv.FormatInt(expiry.Unix(), 10)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) tokenValid(token string) bool {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	if !hmac.Equal(got, mac.Sum(nil)) {
		return false
	}
	expiry, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Unix() < expiry
}

// authorized reports whether the request may read dashboard data.
func (s *Server) authorized(r *http.Request) bool {
	if s.opts.Username == "" {
		return true
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return s.tokenValid(cookie.Value)
}

// ---- websocket ------------------------------------------------------------

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Same-origin check: browsers always send Origin on WebSocket handshakes.
	if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
		http.Error(w, "cross-origin websocket rejected", http.StatusForbidden)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: false,
		CompressionMode:    websocket.CompressionContextTakeover,
	})
	if err != nil {
		s.log.Debug("websocket accept failed", "err", err)
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()
	for {
		if err := s.pushOnce(ctx, conn); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pushInterval):
		}
	}
}

func (s *Server) pushOnce(ctx context.Context, conn *websocket.Conn) error {
	payload, err := json.Marshal(s.opts.State.Snapshot())
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, writeWait)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, payload)
}

func sameOrigin(origin, host string) bool {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(origin, "http://"), "https://")
	return strings.EqualFold(trimmed, host)
}

// ---- static bundle --------------------------------------------------------

func (s *Server) staticHandler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("web: embedded assets missing: " + err.Error())
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			// Fingerprinted-by-build assets: cache them aggressively.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			files.ServeHTTP(w, r)
			return
		}
		// Single page app: every other path serves the shell.
		w.Header().Set("Cache-Control", "no-cache")
		index, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.Error(w, "dashboard bundle missing", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}

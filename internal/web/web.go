// Package web serves the live status dashboard.
//
// The UI is a single embedded HTML document; state reaches the browser over
// Server-Sent Events, which is lighter than a WebSocket for a one-way feed and
// reconnects automatically.
package web

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/sitdownkevin/tjsocks/internal/status"
)

//go:embed index.html
var assets embed.FS

// pushInterval is how often a snapshot is pushed to each client.
const pushInterval = time.Second

// Options configures the dashboard.
type Options struct {
	Addr     string // listen address, empty disables the dashboard
	Username string // optional HTTP basic auth
	Password string
	Log      *slog.Logger
	State    *status.State
}

// Server is the dashboard HTTP server.
type Server struct {
	opts Options
	http *http.Server
}

// New builds the dashboard server.
func New(opts Options) *Server {
	s := &Server{opts: opts}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/events", s.handleEvents)
	s.http = &http.Server{
		Addr:              opts.Addr,
		Handler:           s.requireAuth(mux),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      0, // SSE streams must not be cut by a write deadline
		IdleTimeout:       120 * time.Second,
	}
	return s
}

// ListenAndServe starts the dashboard and stops it when the process ends.
func (s *Server) ListenAndServe() error {
	if s.opts.Addr == "" {
		return nil
	}
	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return fmt.Errorf("dashboard listen on %s: %w", s.opts.Addr, err)
	}
	s.opts.Log.Info("dashboard ready", "url", "http://"+ln.Addr().String(), "auth", s.opts.Username != "")
	if err := s.http.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown stops the dashboard.
func (s *Server) Shutdown() { _ = s.http.Close() }

func (s *Server) requireAuth(next http.Handler) http.Handler {
	if s.opts.Username == "" {
		return next
	}
	wantUser := []byte(s.opts.Username)
	wantPass := []byte(s.opts.Password)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(user), wantUser) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), wantPass) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="tjsocks", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := assets.ReadFile("index.html")
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(page)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.opts.State.Snapshot())
}

// handleEvents streams snapshots over SSE until the client disconnects.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)

	send := func() bool {
		data, err := json.Marshal(s.opts.State.Snapshot())
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !send() {
		return
	}
	ticker := time.NewTicker(pushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

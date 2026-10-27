package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sitdownkevin/tjsocks/internal/status"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func newTestServer(t *testing.T, opts Options) *httptest.Server {
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
	return srv
}

func TestIndexServesHTML(t *testing.T) {
	srv := newTestServer(t, Options{})
	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content type = %q", ct)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "EventSource") {
		t.Fatal("page does not open the event stream")
	}
}

func TestUnknownPathIs404(t *testing.T) {
	srv := newTestServer(t, Options{})
	res, err := http.Get(srv.URL + "/nope")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}

func TestStatusJSON(t *testing.T) {
	st := status.New("v9.9.9", nil)
	st.SetReady("10.1.2.3", []string{"202.120.190.208"}, "0.0.0.0:1080")
	srv := newTestServer(t, Options{State: st})

	res, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type = %q", ct)
	}
	var got status.Snapshot
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Version != "v9.9.9" || got.TunnelIP != "10.1.2.3" || got.Phase != status.PhaseReady {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestBasicAuth(t *testing.T) {
	srv := newTestServer(t, Options{Username: "admin", Password: "secret"})

	for _, tc := range []struct {
		name string
		user string
		pass string
		want int
	}{
		{"no credentials", "", "", http.StatusUnauthorized},
		{"wrong password", "admin", "nope", http.StatusUnauthorized},
		{"wrong user", "root", "secret", http.StatusUnauthorized},
		{"correct", "admin", "secret", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/status", nil)
			if tc.user != "" || tc.pass != "" {
				req.SetBasicAuth(tc.user, tc.pass)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.want)
			}
			if tc.want == http.StatusUnauthorized && res.Header.Get("WWW-Authenticate") == "" {
				t.Error("missing WWW-Authenticate challenge")
			}
		})
	}
}

func TestEventsStreamsSnapshots(t *testing.T) {
	st := status.New("stream-test", nil)
	srv := newTestServer(t, Options{State: st})

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/events", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()

	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	if res.Header.Get("X-Accel-Buffering") != "no" {
		t.Error("reverse proxies may buffer the stream")
	}

	// The first event must arrive immediately, before any ticker fires.
	scanner := bufio.NewScanner(res.Body)
	start := time.Now()
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("first event took %v", elapsed)
		}
		var got status.Snapshot
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Version != "stream-test" {
			t.Fatalf("version = %q", got.Version)
		}
		return
	}
	t.Fatal("stream ended without sending an event")
}

func TestEventsReflectsStateChanges(t *testing.T) {
	st := status.New("dev", nil)
	srv := newTestServer(t, Options{State: st})

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/events", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()

	st.SetReady("10.9.9.9", []string{"1.1.1.1"}, "")

	scanner := bufio.NewScanner(res.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var got status.Snapshot
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &got); err != nil {
			continue
		}
		if got.TunnelIP == "10.9.9.9" && got.Phase == status.PhaseReady {
			return // the stream picked up the new state
		}
	}
	t.Fatal("state change never reached the stream")
}

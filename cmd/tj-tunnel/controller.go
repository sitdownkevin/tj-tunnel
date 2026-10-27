package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/sitdownkevin/tj-tunnel/internal/status"
)

const (
	reconnectMaxDelay = 60 * time.Second
	stableSession     = time.Minute
)

// controller owns the tunnel lifecycle. The proxy listener and dashboard stay
// up permanently; only the VPN session is started and stopped on demand.
type controller struct {
	log *slog.Logger
	st  *status.State

	// session runs one login + tunnel + serve cycle and blocks until it ends.
	session func(ctx context.Context) error

	mu       sync.Mutex
	running  bool
	auto     bool
	cancel   context.CancelFunc
	done     chan struct{}
	attempts int
}

func newController(log *slog.Logger, st *status.State, auto bool, session func(context.Context) error) *controller {
	c := &controller{log: log, st: st, auto: auto, session: session}
	st.SetAutoReconnect(auto)
	st.SetRunning(false)
	return c
}

// Running reports whether a session loop is active.
func (c *controller) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// AutoReconnect reports whether the loop retries after a disconnect.
func (c *controller) AutoReconnect() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.auto
}

// SetAutoReconnect toggles automatic reconnects. Disabling it stops an active
// retry loop, but leaves an established tunnel untouched.
func (c *controller) SetAutoReconnect(enabled bool) {
	c.mu.Lock()
	changed := c.auto != enabled
	c.auto = enabled
	c.mu.Unlock()

	c.st.SetAutoReconnect(enabled)
	if changed {
		c.log.Info("auto reconnect changed", "enabled", enabled)
	}
}

// Start launches the session loop unless it is already running.
func (c *controller) Start() error {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.running = true
	c.cancel = cancel
	c.done = done
	c.attempts = 0
	c.mu.Unlock()

	c.st.SetRunning(true)
	c.st.SetPhase(status.PhaseConnecting)
	c.log.Info("tunnel start requested")

	go func() {
		defer close(done)
		c.loop(ctx)

		c.mu.Lock()
		c.running = false
		c.cancel = nil
		c.mu.Unlock()
		c.st.SetRunning(false)
		c.log.Info("tunnel stopped")
	}()
	return nil
}

// Stop tears the active session down and waits for it to finish.
func (c *controller) Stop() {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	running := c.running
	c.mu.Unlock()

	if !running || cancel == nil {
		return
	}
	c.log.Info("tunnel stop requested")
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		c.log.Warn("tunnel did not stop in time")
	}
}

// loop keeps a session alive for as long as automatic reconnects are enabled.
func (c *controller) loop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}

		c.mu.Lock()
		attempt := c.attempts
		auto := c.auto
		c.mu.Unlock()

		if err == nil {
			err = errors.New("connection closed by peer")
		}
		c.st.SetError(err, attempt)
		c.log.Warn("tunnel closed", "err", err, "uptime", time.Since(started).Round(time.Second))

		if time.Since(started) > stableSession {
			c.mu.Lock()
			c.attempts = 0
			attempt = 0
			c.mu.Unlock()
		}

		if !auto {
			c.st.SetPhase(status.PhaseStopped)
			return
		}

		c.mu.Lock()
		c.attempts++
		attempt = c.attempts
		c.mu.Unlock()

		delay := min(reconnectMaxDelay, time.Duration(1)<<min(attempt, 6)*time.Second)
		c.st.SetPhase(status.PhaseReconnecting)
		c.st.SetError(err, attempt)
		c.log.Warn("reconnecting", "in", delay.String(), "attempt", attempt)

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

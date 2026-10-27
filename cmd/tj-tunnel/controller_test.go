package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sitdownkevin/tj-tunnel/internal/status"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// waitFor polls cond until it is true or the deadline passes.
func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}

func TestControllerStartStop(t *testing.T) {
	st := status.New("test", nil)
	release := make(chan struct{})
	calls := atomic.Int64{}

	ctrl := newController(testLogger(), st, false, func(ctx context.Context) error {
		calls.Add(1)
		select {
		case <-release:
			return errors.New("session ended")
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	if ctrl.Running() {
		t.Fatal("controller should start stopped")
	}
	if st.Snapshot().Running {
		t.Fatal("status should report stopped before start")
	}

	if err := ctrl.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, ctrl.Running, time.Second)
	waitFor(t, func() bool { return calls.Load() == 1 }, time.Second)
	if !st.Snapshot().Running {
		t.Fatal("status should report running")
	}

	// Starting twice must not spawn a second loop.
	if err := ctrl.Start(); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("session ran %d times, want 1", n)
	}

	ctrl.Stop()
	waitFor(t, func() bool { return !ctrl.Running() }, 2*time.Second)
	if st.Snapshot().Running {
		t.Fatal("status should report stopped after Stop")
	}
	if got := st.Snapshot().Phase; got != status.PhaseStopped {
		t.Fatalf("phase = %q, want stopped", got)
	}
}

func TestControllerStopsWhenAutoReconnectDisabled(t *testing.T) {
	st := status.New("test", nil)
	calls := atomic.Int64{}
	ctrl := newController(testLogger(), st, false, func(context.Context) error {
		calls.Add(1)
		return errors.New("boom")
	})

	if err := ctrl.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Without auto-reconnect the loop must give up after the first failure.
	waitFor(t, func() bool { return !ctrl.Running() }, 2*time.Second)
	if n := calls.Load(); n != 1 {
		t.Fatalf("session ran %d times, want 1", n)
	}
	if err := st.Snapshot().LastError; err == "" {
		t.Fatal("last error should be recorded")
	}
}

func TestControllerRetriesWhenAutoReconnectEnabled(t *testing.T) {
	st := status.New("test", nil)
	calls := atomic.Int64{}
	ctrl := newController(testLogger(), st, true, func(context.Context) error {
		calls.Add(1)
		return errors.New("boom")
	})

	if err := ctrl.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// First retry waits 2s (1<<1), so two attempts within 3s proves retrying.
	waitFor(t, func() bool { return calls.Load() >= 2 }, 5*time.Second)
	ctrl.Stop()
	if got := st.Snapshot().Attempts; got < 1 {
		t.Fatalf("attempts = %d, want >= 1", got)
	}
}

func TestControllerAutoReconnectToggle(t *testing.T) {
	st := status.New("test", nil)
	ctrl := newController(testLogger(), st, false, func(context.Context) error { return nil })

	if ctrl.AutoReconnect() {
		t.Fatal("auto reconnect should default to off")
	}
	ctrl.SetAutoReconnect(true)
	if !ctrl.AutoReconnect() || !st.Snapshot().AutoReconnect {
		t.Fatal("auto reconnect not applied")
	}
	ctrl.SetAutoReconnect(false)
	if ctrl.AutoReconnect() || st.Snapshot().AutoReconnect {
		t.Fatal("auto reconnect not cleared")
	}
}

func TestControllerStopIsIdempotent(t *testing.T) {
	st := status.New("test", nil)
	ctrl := newController(testLogger(), st, false, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	ctrl.Stop() // no-op when never started to avoid blocking or panicking
	if err := ctrl.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, ctrl.Running, time.Second)
	ctrl.Stop()
	ctrl.Stop()
}

package logbus

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHistoryIsBoundedAndOrdered(t *testing.T) {
	bus := New(3)
	for _, msg := range []string{"a", "b", "c", "d", "e"} {
		bus.Add(Entry{Time: time.Now(), Level: "INFO", Message: msg})
	}
	got := bus.History()
	if len(got) != 3 {
		t.Fatalf("history length = %d, want 3", len(got))
	}
	for i, want := range []string{"c", "d", "e"} {
		if got[i].Message != want {
			t.Fatalf("history[%d] = %q, want %q", i, got[i].Message, want)
		}
	}
}

func TestHistoryBeforeWrap(t *testing.T) {
	bus := New(5)
	bus.Add(Entry{Message: "only"})
	got := bus.History()
	if len(got) != 1 || got[0].Message != "only" {
		t.Fatalf("history = %+v", got)
	}
}

func TestSubscribeReceivesEntries(t *testing.T) {
	bus := New(10)
	ch, cancel := bus.Subscribe()
	defer cancel()

	bus.Add(Entry{Message: "hello"})
	select {
	case entry := <-ch:
		if entry.Message != "hello" {
			t.Fatalf("entry = %+v", entry)
		}
	case <-time.After(time.Second):
		t.Fatal("no entry received")
	}
}

func TestCancelClosesChannelAndStopsDelivery(t *testing.T) {
	bus := New(10)
	ch, cancel := bus.Subscribe()
	cancel()

	if _, open := <-ch; open {
		t.Fatal("channel should be closed after cancel")
	}
	// Publishing after cancel must not panic.
	bus.Add(Entry{Message: "after"})
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	bus := New(4)
	_, cancel := bus.Subscribe() // never read
	defer cancel()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			bus.Add(Entry{Message: "spam"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Add blocked on a slow subscriber")
	}
}

func TestConcurrentPublishAndSubscribe(t *testing.T) {
	bus := New(50)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				bus.Add(Entry{Message: "x"})
				_ = bus.History()
			}
		}()
	}
	wg.Wait()
}

func TestHandlerMirrorsRecords(t *testing.T) {
	bus := New(10)
	var out strings.Builder
	base := slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(NewHandler(base, bus))

	logger.Info("started", "version", "1.2.3")
	logger.Error("boom", "err", "connection reset")

	history := bus.History()
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}
	if history[0].Level != "INFO" || history[0].Message != "started" {
		t.Fatalf("entry = %+v", history[0])
	}
	if len(history[0].Attrs) != 1 || history[0].Attrs[0] != "version=1.2.3" {
		t.Fatalf("attrs = %v", history[0].Attrs)
	}
	if history[1].Level != "ERROR" {
		t.Fatalf("level = %q, want ERROR", history[1].Level)
	}
	// The wrapped handler still received everything.
	if !strings.Contains(out.String(), "boom") {
		t.Fatal("wrapped handler did not receive the record")
	}
}

func TestHandlerWithAttrsAndGroups(t *testing.T) {
	bus := New(10)
	base := slog.NewTextHandler(discard{}, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(NewHandler(base, bus)).With("component", "tunnel")

	logger.Warn("retrying", "attempt", 3)

	history := bus.History()
	if len(history) != 1 {
		t.Fatalf("history = %+v", history)
	}
	attrs := history[0].Attrs
	if len(attrs) != 2 || attrs[0] != "component=tunnel" || attrs[1] != "attempt=3" {
		t.Fatalf("attrs = %v", attrs)
	}
}

func TestHandlerRespectsLevel(t *testing.T) {
	bus := New(10)
	base := slog.NewTextHandler(discard{}, &slog.HandlerOptions{Level: slog.LevelWarn})
	logger := slog.New(NewHandler(base, bus))

	logger.Info("dropped")
	logger.Warn("kept")

	history := bus.History()
	if len(history) != 1 || history[0].Message != "kept" {
		t.Fatalf("history = %+v", history)
	}
}

func TestHandlerIsSafeForConcurrentUse(t *testing.T) {
	bus := New(100)
	base := slog.NewTextHandler(discard{}, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(NewHandler(base, bus))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				logger.Info("concurrent", "i", j)
			}
		}()
	}
	wg.Wait()

	if got := len(bus.History()); got != 100 {
		t.Fatalf("history length = %d, want 100 (ring size)", got)
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

var _ = context.Background

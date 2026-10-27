// Package logbus fans slog records out to the dashboard.
//
// It keeps a bounded ring of recent entries for clients that just connected and
// broadcasts new records to every live subscriber. Publishing never blocks the
// caller: a subscriber that cannot keep up simply loses records instead of
// stalling the tunnel.
package logbus

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Entry is one log record as shown in the dashboard.
type Entry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Attrs   []string  `json:"attrs,omitempty"`
}

// Bus stores recent entries and broadcasts new ones.
type Bus struct {
	mu      sync.Mutex
	ring    []Entry
	size    int
	next    int
	full    bool
	subs    map[chan Entry]struct{}
	dropped int64
}

// New creates a bus keeping up to size entries.
func New(size int) *Bus {
	if size <= 0 {
		size = 200
	}
	return &Bus{ring: make([]Entry, size), size: size, subs: make(map[chan Entry]struct{})}
}

// Add appends an entry and notifies subscribers.
func (b *Bus) Add(e Entry) {
	b.mu.Lock()
	b.ring[b.next] = e
	b.next = (b.next + 1) % b.size
	if b.next == 0 {
		b.full = true
	}
	subs := make([]chan Entry, 0, len(b.subs))
	for ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- e:
		default:
			// Slow client: drop rather than blocking the logger.
		}
	}
}

// History returns buffered entries, oldest first.
func (b *Bus) History() []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.full {
		out := make([]Entry, b.next)
		copy(out, b.ring[:b.next])
		return out
	}
	out := make([]Entry, 0, b.size)
	out = append(out, b.ring[b.next:]...)
	out = append(out, b.ring[:b.next]...)
	return out
}

// Subscribe returns a channel of new entries and a cancel function.
func (b *Bus) Subscribe() (<-chan Entry, func()) {
	ch := make(chan Entry, 128)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
	return ch, cancel
}

// Handler is a slog.Handler that tees records to another handler and the bus.
type Handler struct {
	next  slog.Handler
	bus   *Bus
	attrs []slog.Attr
	group string
}

// NewHandler wraps next, mirroring every record into bus.
func NewHandler(next slog.Handler, bus *Bus) *Handler {
	return &Handler{next: next, bus: bus}
}

// Enabled delegates to the wrapped handler.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle writes to the wrapped handler and publishes to the bus.
func (h *Handler) Handle(ctx context.Context, record slog.Record) error {
	attrs := make([]string, 0, len(h.attrs)+record.NumAttrs())
	for _, a := range h.attrs {
		attrs = appendAttr(attrs, a)
	}
	record.Attrs(func(a slog.Attr) bool {
		attrs = appendAttr(attrs, a)
		return true
	})
	h.bus.Add(Entry{
		Time:    record.Time,
		Level:   levelName(record.Level),
		Message: record.Message,
		Attrs:   attrs,
	})
	return h.next.Handle(ctx, record)
}

// WithAttrs returns a handler with extra attributes.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.next = h.next.WithAttrs(attrs)
	clone.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &clone
}

// WithGroup returns a handler that prefixes group names.
func (h *Handler) WithGroup(name string) slog.Handler {
	clone := *h
	clone.next = h.next.WithGroup(name)
	if name != "" {
		if clone.group == "" {
			clone.group = name
		} else {
			clone.group += "." + name
		}
	}
	return &clone
}

func appendAttr(dst []string, a slog.Attr) []string {
	if a.Equal(slog.Attr{}) {
		return dst
	}
	value := a.Value.Resolve()
	if value.Kind() == slog.KindGroup {
		for _, inner := range value.Group() {
			dst = appendAttr(dst, inner)
		}
		return dst
	}
	key := a.Key
	if key == "" {
		return dst
	}
	text := value.String()
	if strings.ContainsAny(text, " \t\n") {
		text = strings.ReplaceAll(text, "\n", " ")
	}
	return append(dst, key+"="+text)
}

func levelName(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "ERROR"
	case level >= slog.LevelWarn:
		return "WARN"
	case level >= slog.LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}

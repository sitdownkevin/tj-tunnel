package resolver

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func TestLookupParsesIPLiteralsWithoutQuery(t *testing.T) {
	// A resolver with a nil stack and nil cache is fine as long as no DNS
	// query is issued: IP literals are returned directly.
	r := &Resolver{cache: map[string]entry{}}
	got, err := r.Lookup(context.Background(), "192.168.80.60")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.String() != "192.168.80.60" {
		t.Fatalf("got %v", got)
	}
}

func TestLookupRejectsIPv6Targets(t *testing.T) {
	r := &Resolver{cache: map[string]entry{}}
	if _, err := r.Lookup(context.Background(), "2001:db8::1"); err == nil {
		t.Fatal("expected error for IPv6 literal")
	}
}

func TestIPv4MappedLiteralIsUnmapped(t *testing.T) {
	r := &Resolver{cache: map[string]entry{}}
	got, err := r.Lookup(context.Background(), "::ffff:10.0.0.1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !got.Is4() {
		t.Fatalf("got %v, want an IPv4 address", got)
	}
}

func TestCacheHonoursExpiry(t *testing.T) {
	r := &Resolver{cache: map[string]entry{}}
	want := netip.MustParseAddr("10.1.2.3")
	r.store("example.test", want, 50*time.Millisecond)

	if got, ok := r.cached("example.test"); !ok || got != want {
		t.Fatalf("cached = %v, %v", got, ok)
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := r.cached("example.test"); ok {
		t.Fatal("entry should have expired")
	}
}

func TestCacheConcurrentAccess(t *testing.T) {
	r := &Resolver{cache: map[string]entry{}}
	addr := netip.MustParseAddr("10.0.0.1")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.store("host.test", addr, time.Minute)
				_, _ = r.cached("host.test")
			}
		}()
	}
	wg.Wait()
}

func TestNewRejectsEmptyServerList(t *testing.T) {
	if _, err := New(nil, nil); err == nil {
		t.Fatal("expected error without DNS servers")
	}
}

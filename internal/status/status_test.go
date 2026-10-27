package status

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeTraffic is a controllable socks.Counters stand-in.
type fakeTraffic struct {
	total, up, down, active int64
	mu                      sync.Mutex
}

func (f *fakeTraffic) Snapshot() (int64, int64, int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.total, f.up, f.down
}

func (f *fakeTraffic) ActiveConnections() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active
}

func (f *fakeTraffic) add(up, down int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.up += up
	f.down += down
}

func TestInitialSnapshot(t *testing.T) {
	s := New("1.2.3", &fakeTraffic{})
	got := s.Snapshot()
	if got.Version != "1.2.3" {
		t.Errorf("version = %q", got.Version)
	}
	if got.Phase != PhaseStarting {
		t.Errorf("phase = %q, want starting", got.Phase)
	}
	if got.SessionUptime != 0 {
		t.Errorf("session uptime = %v, want 0", got.SessionUptime)
	}
}

func TestReadyTransitionsAndReset(t *testing.T) {
	s := New("dev", &fakeTraffic{})
	s.SetConfig("vpn.tongji.cn", "LDAP3", ":1080", ":1080", true)
	s.SetError(errors.New("boom"), 3)
	s.SetReady("10.1.2.3", []string{"202.120.190.208"}, "0.0.0.0:1080")

	got := s.Snapshot()
	if got.Phase != PhaseReady || got.TunnelIP != "10.1.2.3" {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.Attempts != 0 || got.LastError != "" {
		t.Errorf("ready should clear errors: attempts=%d err=%q", got.Attempts, got.LastError)
	}
	if !got.UDPLive || got.UDPRelay != "0.0.0.0:1080" {
		t.Errorf("udp state = %+v", got)
	}
	if !got.ProxyAuth {
		t.Error("proxy auth flag not propagated")
	}

	s.SetPhase(PhaseReconnecting)
	got = s.Snapshot()
	if got.SessionUptime != 0 {
		t.Errorf("session uptime should reset, got %v", got.SessionUptime)
	}
	if got.UDPLive {
		t.Error("UDP should be marked down while reconnecting")
	}
}

func TestDNSIsCopied(t *testing.T) {
	s := New("dev", nil)
	dns := []string{"1.1.1.1"}
	s.SetReady("10.0.0.1", dns, "")
	dns[0] = "mutated"
	if got := s.Snapshot().DNS[0]; got != "1.1.1.1" {
		t.Fatalf("snapshot aliases caller slice: %q", got)
	}
}

func TestRatesUseTrafficDelta(t *testing.T) {
	ft := &fakeTraffic{}
	s := New("dev", ft)
	s.Snapshot() // establish the sampling baseline

	ft.add(2048, 4096)
	time.Sleep(20 * time.Millisecond)
	got := s.Snapshot()

	if got.UpTotal != 2048 || got.DownTotal != 4096 {
		t.Fatalf("totals = %d/%d", got.UpTotal, got.DownTotal)
	}
	if got.UpRate <= 0 || got.DownRate <= 0 {
		t.Fatalf("rates = %v/%v, want positive", got.UpRate, got.DownRate)
	}
	if got.DownRate < got.UpRate {
		t.Errorf("down rate (%v) should exceed up rate (%v)", got.DownRate, got.UpRate)
	}
}

func TestRatesIgnoreCounterReset(t *testing.T) {
	ft := &fakeTraffic{up: 1000, down: 1000}
	s := New("dev", ft)
	s.Snapshot()

	ft.mu.Lock()
	ft.up, ft.down = 0, 0 // e.g. a new session reset the counters
	ft.mu.Unlock()
	time.Sleep(10 * time.Millisecond)

	got := s.Snapshot()
	if got.UpRate != 0 && got.DownRate != 0 {
		// A negative delta must never surface as a negative rate.
		if got.UpRate < 0 || got.DownRate < 0 {
			t.Fatalf("negative rates: %v/%v", got.UpRate, got.DownRate)
		}
	}
}

func TestActiveConnectionsReported(t *testing.T) {
	ft := &fakeTraffic{active: 7, total: 42}
	s := New("dev", ft)
	got := s.Snapshot()
	if got.ActiveConnections != 7 || got.Connections != 42 {
		t.Fatalf("connections = %d active / %d total", got.ActiveConnections, got.Connections)
	}
}

func TestConcurrentUse(t *testing.T) {
	s := New("dev", &fakeTraffic{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				switch j % 4 {
				case 0:
					s.SetPhase(PhaseConnecting)
				case 1:
					s.SetReady("10.0.0.1", []string{"1.1.1.1"}, "0.0.0.0:1080")
				case 2:
					s.SetError(errors.New("x"), j)
				default:
					_ = s.Snapshot()
				}
			}
		}(i)
	}
	wg.Wait()
}

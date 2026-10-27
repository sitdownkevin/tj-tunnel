// Package status keeps the live state of the proxy so it can be read by the
// web dashboard without touching the hot path of the data plane.
package status

import (
	"sync"
	"time"
)

// Phase is the lifecycle stage of the tunnel.
type Phase string

const (
	PhaseStarting     Phase = "starting"     // process started, nothing attempted yet
	PhaseConnecting   Phase = "connecting"   // logging in to the gateway
	PhaseReady        Phase = "ready"        // tunnel up, proxy serving
	PhaseReconnecting Phase = "reconnecting" // tunnel dropped, backing off
	PhaseStopped      Phase = "stopped"      // shutting down
)

// Traffic is implemented by socks.Counters.
type Traffic interface {
	Snapshot() (total, up, down int64)
}

// Active is optionally implemented to report open connections.
type Active interface {
	ActiveConnections() int64
}

// Snapshot is the JSON document served to the dashboard.
type Snapshot struct {
	Version string `json:"version"`
	Phase   Phase  `json:"phase"`

	// Timings, in seconds.
	Uptime        float64 `json:"uptime"`
	SessionUptime float64 `json:"session_uptime"`

	// Tunnel details (zero values until the first successful login).
	Gateway    string   `json:"gateway"`
	AuthMethod string   `json:"auth_method"`
	TunnelIP   string   `json:"tunnel_ip"`
	DNS        []string `json:"dns"`
	Listen     string   `json:"listen"`
	UDPListen  string   `json:"udp_listen"`
	UDPRelay   string   `json:"udp_relay"`

	// Traffic.
	Connections       int64   `json:"connections"`
	ActiveConnections int64   `json:"active_connections"`
	UpTotal           int64   `json:"up_total"`
	DownTotal         int64   `json:"down_total"`
	UpRate            float64 `json:"up_rate"`
	DownRate          float64 `json:"down_rate"`

	// Reconnect bookkeeping.
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error"`

	// Config flags, useful in the UI.
	ProxyAuth bool `json:"proxy_auth"`
	UDPLive   bool `json:"udp_live"`
}

// State is safe for concurrent use.
type State struct {
	mu sync.Mutex

	version string
	started time.Time

	phase      Phase
	gateway    string
	authMethod string
	tunnelIP   string
	dns        []string
	listen     string
	udpListen  string
	udpRelay   string
	sessionAt  time.Time
	attempts   int
	lastError  string
	proxyAuth  bool
	udpLive    bool

	traffic Traffic

	// Rate sampling state.
	sampled  time.Time
	lastUp   int64
	lastDown int64
	upRate   float64
	downRate float64
}

// New creates the process-wide state.
func New(version string, traffic Traffic) *State {
	return &State{
		version: version,
		started: time.Now(),
		phase:   PhaseStarting,
		traffic: traffic,
		sampled: time.Now(),
	}
}

// SetConfig records the static configuration shown in the dashboard.
func (s *State) SetConfig(gateway, authMethod, listen, udpListen string, proxyAuth bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gateway = gateway
	s.authMethod = authMethod
	s.listen = listen
	s.udpListen = udpListen
	s.proxyAuth = proxyAuth
}

// SetPhase moves the tunnel to another lifecycle stage.
func (s *State) SetPhase(p Phase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = p
	if p == PhaseConnecting || p == PhaseReconnecting {
		s.sessionAt = time.Time{}
		s.udpRelay = ""
		s.udpLive = false
	}
}

// SetReady publishes the details of a freshly established tunnel.
func (s *State) SetReady(tunnelIP string, dns []string, udpRelay string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = PhaseReady
	s.tunnelIP = tunnelIP
	s.dns = append([]string(nil), dns...) // copy: the caller may reuse its slice
	s.sessionAt = time.Now()
	s.attempts = 0
	s.lastError = ""
	s.udpRelay = udpRelay
	s.udpLive = udpRelay != ""
}

// SetError records why the last attempt failed.
func (s *State) SetError(err error, attempts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = attempts
	if err != nil {
		s.lastError = err.Error()
	}
}

// Snapshot returns a consistent copy of the current state, including the
// transfer rates since the previous call.
func (s *State) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var total, up, down, active int64
	if s.traffic != nil {
		total, up, down = s.traffic.Snapshot()
		if a, ok := s.traffic.(Active); ok {
			active = a.ActiveConnections()
		}
	}
	if elapsed := now.Sub(s.sampled).Seconds(); elapsed > 0 {
		// Smooth the instantaneous rate so the dashboard does not flicker.
		upNow := float64(up-s.lastUp) / elapsed
		downNow := float64(down-s.lastDown) / elapsed
		if upNow < 0 {
			upNow = 0
		}
		if downNow < 0 {
			downNow = 0
		}
		s.upRate = 0.6*upNow + 0.4*s.upRate
		s.downRate = 0.6*downNow + 0.4*s.downRate
	}
	s.sampled, s.lastUp, s.lastDown = now, up, down

	snapshot := Snapshot{
		Version:           s.version,
		Phase:             s.phase,
		Uptime:            now.Sub(s.started).Seconds(),
		Gateway:           s.gateway,
		AuthMethod:        s.authMethod,
		TunnelIP:          s.tunnelIP,
		DNS:               append([]string(nil), s.dns...),
		Listen:            s.listen,
		UDPListen:         s.udpListen,
		UDPRelay:          s.udpRelay,
		Connections:       total,
		ActiveConnections: active,
		UpTotal:           up,
		DownTotal:         down,
		UpRate:            s.upRate,
		DownRate:          s.downRate,
		Attempts:          s.attempts,
		LastError:         s.lastError,
		ProxyAuth:         s.proxyAuth,
		UDPLive:           s.udpLive,
	}
	if !s.sessionAt.IsZero() {
		snapshot.SessionUptime = now.Sub(s.sessionAt).Seconds()
	}
	return snapshot
}

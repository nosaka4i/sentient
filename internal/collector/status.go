package collector

import (
	"sync"
	"time"
)

// State is where a collector's connection to its source stands.
type State string

const (
	Connecting   State = "connecting"   // started, no answer yet
	Connected    State = "connected"    // events are flowing
	Reconnecting State = "reconnecting" // the source was unreachable or the stream ended; retrying
)

// rateSeconds is the window the event rate is averaged over.
const rateSeconds = 60

// Status is what a collector knows about itself, for the Health page. It is
// the same for every platform's collector (Tetragon, eslogger, ...): the
// collector writes it, anyone may read it. What is platform-specific is only
// the words: System, Platform and the one Detail line (a socket, a process).
type Status struct {
	system, platform string
	detailLabel      string
	detail           string

	mu        sync.Mutex
	state     State
	since     time.Time // when the state last changed
	lastErr   string
	version   string
	reconnect int64
	events    int64
	lastEvent time.Time
	buckets   [rateSeconds]int64 // events per second, a ring keyed by unix second
	bucketSec [rateSeconds]int64
}

// Snapshot is a copy of a Status at one moment.
type Snapshot struct {
	System      string // "Tetragon", "eslogger"
	Platform    string // "eBPF on Linux", "Endpoint Security"
	DetailLabel string // "socket", "process"
	Detail      string // "unix:///var/run/tetragon/tetragon.sock", "eslogger, pid 4182"
	Version     string // the source's own version; empty if it did not say

	State      State
	Since      time.Time // when the state last changed: capturing since, when Connected
	LastError  string    // why the source is not reachable; empty while connected
	Reconnects int64     // times the connection came back after being lost
	Events     int64     // events read since sentientd started
	LastEvent  time.Time
	PerMinute  float64 // events over the last minute
}

// NewStatus returns the status of a collector that starts connecting at now.
func NewStatus(system, platform, detailLabel, detail string, now time.Time) *Status {
	return &Status{system: system, platform: platform, detailLabel: detailLabel, detail: detail,
		state: Connecting, since: now}
}

// SetState records a change of state; err is why, when it is not Connected.
func (s *Status) SetState(st State, now time.Time, err string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != st {
		if st == Connected && s.state == Reconnecting {
			s.reconnect++
		}
		s.state, s.since = st, now
	}
	s.lastErr = err
	if st == Connected {
		s.lastErr = ""
	}
}

// SetVersion records the source's own version.
func (s *Status) SetVersion(v string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.version = v
	s.mu.Unlock()
}

// CountEvent counts one event read from the source.
func (s *Status) CountEvent(now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events++
	s.lastEvent = now
	sec := now.Unix()
	i := int(sec % rateSeconds)
	if s.bucketSec[i] != sec {
		s.bucketSec[i], s.buckets[i] = sec, 0
	}
	s.buckets[i]++
}

// Snapshot returns the current state. now ages the rate window.
func (s *Status) Snapshot(now time.Time) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for i := range s.buckets {
		if age := now.Unix() - s.bucketSec[i]; age >= 0 && age < rateSeconds {
			n += s.buckets[i]
		}
	}
	return Snapshot{
		System: s.system, Platform: s.platform, DetailLabel: s.detailLabel, Detail: s.detail, Version: s.version,
		State: s.state, Since: s.since, LastError: s.lastErr, Reconnects: s.reconnect,
		Events: s.events, LastEvent: s.lastEvent, PerMinute: float64(n),
	}
}

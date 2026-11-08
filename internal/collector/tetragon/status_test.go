package tetragon

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/collector"
)

func TestStatusFollowsTheConnection(t *testing.T) {
	// a closed port first, Tetragon started on it afterwards
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()

	st := NewStatus(addr, time.Now())
	if s := st.Snapshot(time.Now()); s.State != collector.Connecting || s.Detail != addr || s.System != "Tetragon" || s.DetailLabel != "socket" {
		t.Fatalf("at the start: %+v", s)
	}
	c := newCollector(addr)
	c.Status = st
	stop := runCollector(t, c, newRecorder())

	until(t, "reconnecting", func() bool { return st.Snapshot(time.Now()).State == collector.Reconnecting })
	s := st.Snapshot(time.Now())
	if !strings.Contains(s.LastError, "connect to "+addr) || !strings.Contains(s.LastError, "refused") {
		t.Errorf("last error = %q", s.LastError)
	}
	if s.Reconnects != 0 {
		t.Errorf("reconnects = %d before it ever connected", s.Reconnects)
	}

	f := &fakeTetragon{events: fixture(t), hold: true, release: make(chan struct{}), version: "1.7.1"}
	serve(t, f, addr)
	until(t, "connected with events", func() bool {
		s := st.Snapshot(time.Now())
		return s.State == collector.Connected && s.Events >= 10 && s.Version != ""
	})
	s = st.Snapshot(time.Now())
	if s.LastError != "" || s.Version != "1.7.1" || s.Reconnects != 1 || s.LastEvent.IsZero() || s.PerMinute < 10 {
		t.Errorf("connected: %+v", s)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestStatusWhenTheStreamEnds(t *testing.T) {
	f := &fakeTetragon{events: fixture(t)[:1]} // one event, then the stream ends; no version
	addr, _ := serve(t, f, "127.0.0.1:0")
	st := NewStatus(addr, time.Now())
	c := newCollector(addr)
	c.Status = st
	stop := runCollector(t, c, newRecorder())
	// the reconnect is counted when the second stream opens; its event is read a moment later
	until(t, "a reconnect and the event after it", func() bool {
		s := st.Snapshot(time.Now())
		return s.Reconnects >= 1 && s.Events >= 2
	})
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if s := st.Snapshot(time.Now()); s.Version != "" || s.Events < 2 {
		t.Errorf("no version answered, events read twice: %+v", s)
	}
}

func TestNewStatusDefaultsTheAddress(t *testing.T) {
	if s := NewStatus("", time.Now()).Snapshot(time.Now()); s.Detail != DefaultAddr {
		t.Errorf("detail = %q", s.Detail)
	}
}

func TestDialError(t *testing.T) {
	if got := dialError("unix://" + filepath.Join(t.TempDir(), "none.sock")); !strings.Contains(got, "no such file") {
		t.Errorf("missing socket: %q", got)
	}
	if got := dialError("unix:" + filepath.Join(t.TempDir(), "none.sock")); !strings.Contains(got, "no such file") {
		t.Errorf("unix: form: %q", got)
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	if got := dialError(addr); got != "not answering" {
		t.Errorf("a port that accepts: %q", got)
	}
	l.Close()
	if got := dialError(addr); !strings.Contains(got, "refused") {
		t.Errorf("a closed port: %q", got)
	}
}

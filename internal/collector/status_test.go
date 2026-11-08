package collector

import (
	"testing"
	"time"
)

func TestStatusNamesTheSourceAndFollowsTheState(t *testing.T) {
	t0 := time.Date(2026, 11, 5, 4, 5, 0, 0, time.UTC)
	st := NewStatus("Tetragon", "eBPF on Linux", "socket", "unix:///x.sock", t0)
	s := st.Snapshot(t0)
	if s.System != "Tetragon" || s.Platform != "eBPF on Linux" || s.DetailLabel != "socket" || s.Detail != "unix:///x.sock" ||
		s.State != Connecting || !s.Since.Equal(t0) {
		t.Fatalf("at the start: %+v", s)
	}

	st.SetState(Reconnecting, t0.Add(time.Second), "connect: refused")
	s = st.Snapshot(t0.Add(time.Second))
	if s.State != Reconnecting || s.LastError != "connect: refused" || s.Reconnects != 0 {
		t.Errorf("lost before it ever connected: %+v", s)
	}
	// the same state again keeps its start but takes the new reason
	st.SetState(Reconnecting, t0.Add(5*time.Second), "connect: still refused")
	if s = st.Snapshot(t0.Add(5 * time.Second)); !s.Since.Equal(t0.Add(time.Second)) || s.LastError != "connect: still refused" {
		t.Errorf("repeat: %+v", s)
	}

	st.SetState(Connected, t0.Add(10*time.Second), "")
	st.SetVersion("1.7.1")
	s = st.Snapshot(t0.Add(10 * time.Second))
	if s.State != Connected || s.LastError != "" || s.Reconnects != 1 || s.Version != "1.7.1" || !s.Since.Equal(t0.Add(10*time.Second)) {
		t.Errorf("connected: %+v", s)
	}

	// losing it again and coming back is a second reconnect
	st.SetState(Reconnecting, t0.Add(20*time.Second), "stream ended")
	st.SetState(Connected, t0.Add(22*time.Second), "")
	if s = st.Snapshot(t0.Add(22 * time.Second)); s.Reconnects != 2 {
		t.Errorf("reconnects = %d", s.Reconnects)
	}
}

func TestStatusRateAgesOut(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	st := NewStatus("x", "", "", "", now)
	for i := 0; i < 30; i++ {
		st.CountEvent(now.Add(time.Duration(i) * time.Second))
	}
	if s := st.Snapshot(now.Add(30 * time.Second)); s.PerMinute != 30 || s.Events != 30 || !s.LastEvent.Equal(now.Add(29*time.Second)) {
		t.Errorf("a minute of events: %+v", s)
	}
	if s := st.Snapshot(now.Add(2 * time.Minute)); s.PerMinute != 0 || s.Events != 30 {
		t.Errorf("two minutes later the rate is 0 and the total stays: %+v", s)
	}
	// a second reused by the ring starts from zero
	st.CountEvent(now.Add(60 * time.Second))
	st.CountEvent(now.Add(60 * time.Second))
	if s := st.Snapshot(now.Add(60 * time.Second)); s.PerMinute != 31 {
		t.Errorf("ring reuse: %+v", s)
	}
}

func TestStatusIsNilSafe(t *testing.T) {
	var st *Status
	st.SetState(Connected, time.Now(), "")
	st.SetVersion("1")
	st.CountEvent(time.Now()) // a collector without a status must not panic
}

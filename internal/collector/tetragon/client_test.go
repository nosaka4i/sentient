package tetragon

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cilium/tetragon/api/v1/tetragon"
	"google.golang.org/grpc"

	"github.com/nosaka4i/sentient/internal/collector/tetragon/policy"
)

// fakeTetragon streams a script of events to each GetEvents caller.
type fakeTetragon struct {
	tetragon.UnimplementedFineGuidanceSensorsServer
	events  []*tetragon.GetEventsResponse
	hold    bool // keep the stream open after the script (a live Tetragon)
	calls   atomic.Int32
	gotReq  atomic.Pointer[tetragon.GetEventsRequest]
	release chan struct{} // closed to end held streams
}

func (f *fakeTetragon) GetEvents(req *tetragon.GetEventsRequest, s grpc.ServerStreamingServer[tetragon.GetEventsResponse]) error {
	f.calls.Add(1)
	f.gotReq.Store(req)
	for _, e := range f.events {
		if err := s.Send(e); err != nil {
			return err
		}
	}
	if f.hold {
		select {
		case <-s.Context().Done():
		case <-f.release:
		}
	}
	return nil
}

func serve(t *testing.T, f *fakeTetragon, addr string) (string, *grpc.Server) {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	tetragon.RegisterFineGuidanceSensorsServer(srv, f)
	go srv.Serve(l)
	t.Cleanup(srv.Stop)
	return l.Addr().String(), srv
}

func newCollector(addr string) *Collector {
	return &Collector{Addr: addr, Agents: policy.AgentPrefixes([]string{"/home/ubuntu"}), Retry: 10 * time.Millisecond, ConnWait: 300 * time.Millisecond}
}

// until polls cond for up to two seconds.
func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *recorder) counts() (procs, events, gaps int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.procs), len(r.events), len(r.gaps)
}

func (r *recorder) gap(i int) gapRec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gaps[i]
}

// startCollector runs c in the background; done receives Run's result.
func startCollector(t *testing.T, c *Collector, rec *recorder) (cancel func(), done chan error) {
	t.Helper()
	cctx, cancel := context.WithCancel(ctx)
	done = make(chan error, 1)
	go func() { done <- c.Run(cctx, rec) }()
	t.Cleanup(cancel)
	return cancel, done
}

// runCollector is startCollector with a stop that cancels and waits for Run.
func runCollector(t *testing.T, c *Collector, rec *recorder) (stop func() error) {
	t.Helper()
	cancel, done := startCollector(t, c, rec)
	return func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(3 * time.Second):
			t.Fatal("Run did not return after cancel")
			return nil
		}
	}
}

// returnsOnItsOwn waits for Run to return without being cancelled.
func returnsOnItsOwn(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("Run kept going; it should have returned the sink's error")
		return nil
	}
}

func TestCollectorReadsTheStream(t *testing.T) {
	f := &fakeTetragon{events: fixture(t), hold: true, release: make(chan struct{})}
	addr, _ := serve(t, f, "127.0.0.1:0")
	rec := newRecorder()
	c := newCollector(addr)
	if c.Name() != "tetragon" {
		t.Errorf("name = %q", c.Name())
	}
	stop := runCollector(t, c, rec)
	until(t, "the fixture to be recorded", func() bool { p, e, _ := rec.counts(); return p == 2 && e >= 10 })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if _, _, gaps := rec.counts(); gaps != 0 {
		t.Errorf("%d gaps on a healthy stream", gaps)
	}
	// only the three event kinds we use are requested
	req := f.gotReq.Load()
	if req == nil || len(req.AllowList) != 1 || len(req.AllowList[0].EventSet) != 3 {
		t.Errorf("request = %+v", req)
	}
}

func TestCollectorRecordsAGapWhileTetragonIsUnreachable(t *testing.T) {
	// find a free port, leave it closed, start Tetragon on it later
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()

	rec := newRecorder()
	times := []time.Time{time.Date(2026, 10, 22, 4, 0, 0, 0, time.UTC), time.Date(2026, 10, 22, 4, 5, 0, 0, time.UTC)}
	var tick atomic.Int32
	c := newCollector(addr)
	c.Now = func() time.Time { return times[min(int(tick.Add(1))-1, len(times)-1)] }
	stop := runCollector(t, c, rec)
	until(t, "a gap to open", func() bool { _, _, g := rec.counts(); return g == 1 })
	if g := rec.gap(0); !strings.Contains(g.reason, "unreachable") || g.closedFlag {
		t.Errorf("gap = %+v", g)
	}

	f := &fakeTetragon{events: fixture(t), hold: true, release: make(chan struct{})}
	serve(t, f, addr)
	until(t, "the gap to close", func() bool { return rec.gap(0).closedFlag })
	until(t, "events to arrive", func() bool { p, _, _ := rec.counts(); return p == 2 })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if _, _, gaps := rec.counts(); gaps != 1 {
		t.Errorf("%d gaps, want one that was closed", gaps)
	}
	if g := rec.gap(0); !g.closed.After(g.opened) {
		t.Errorf("gap opened %v closed %v", g.opened, g.closed)
	}
}

func TestCollectorRecordsAGapWhenTheStreamEndsAndReconnects(t *testing.T) {
	f := &fakeTetragon{events: fixture(t)[:1]} // sends one event, then the stream ends
	addr, _ := serve(t, f, "127.0.0.1:0")
	rec := newRecorder()
	stop := runCollector(t, newCollector(addr), rec)
	// it reconnects and re-reads each time; a gap opens and closes around each
	until(t, "a second connection", func() bool { return f.calls.Load() >= 2 })
	until(t, "a closed gap", func() bool { _, _, g := rec.counts(); return g >= 1 && rec.gap(0).closedFlag })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if g := rec.gap(0); !strings.Contains(g.reason, "stream ended") {
		t.Errorf("reason = %q", g.reason)
	}
}

func TestCollectorStopsWhenTheSinkFails(t *testing.T) {
	f := &fakeTetragon{events: fixture(t), hold: true, release: make(chan struct{})}
	addr, _ := serve(t, f, "127.0.0.1:0")
	rec := newRecorder()
	rec.failOn = "process"
	_, done := startCollector(t, newCollector(addr), rec)
	if err := returnsOnItsOwn(t, done); err != errFail {
		t.Errorf("Run returned %v, want the sink's error", err)
	}
}

func TestCollectorReturnsSinkErrorsFromGaps(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	rec := newRecorder()
	rec.failOn = "gap"
	_, done := startCollector(t, newCollector(addr), rec)
	if err := returnsOnItsOwn(t, done); err != errFail {
		t.Errorf("Run returned %v, want the sink's error", err)
	}
}

func TestCollectorDefaults(t *testing.T) {
	var c Collector
	if c.addr() != DefaultAddr || c.retry() != 2*time.Second || c.connWait() != 3*time.Second {
		t.Errorf("defaults: %q %v %v", c.addr(), c.retry(), c.connWait())
	}
	if c.now().IsZero() {
		t.Error("now")
	}
}

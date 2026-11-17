package collector

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/collector/tetragon/policy"
	"github.com/nosaka4i/sentient/internal/store"
)

// logSink records the order of everything it is given.
type logSink struct {
	log  []string
	evts []store.KernelEvent
	fail error
}

func (l *logSink) Process(context.Context, store.Process) error { return nil }
func (l *logSink) Exit(_ context.Context, id string, _ time.Time, _ int) error {
	l.log = append(l.log, "exit "+id)
	return l.fail
}
func (l *logSink) Event(_ context.Context, e store.KernelEvent) error {
	l.log = append(l.log, e.Type+" "+e.Path+" "+e.Payload)
	l.evts = append(l.evts, e)
	return l.fail
}
func (l *logSink) GapOpened(context.Context, time.Time, string) (int64, error) {
	l.log = append(l.log, "gap")
	return 1, l.fail
}
func (l *logSink) GapClosed(context.Context, int64, time.Time) error { return nil }

func write(exec, path, ts string) store.KernelEvent {
	return store.KernelEvent{ExecID: exec, Type: "file_write", Path: path, TS: ts}
}

func TestCoalesceRepeatedWritesUntilExit(t *testing.T) {
	out := &logSink{}
	c := &CoalesceSink{Sink: out, Level: policy.Essential}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := c.Event(ctx, write("p1", "/a", fmt.Sprintf("t%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	// written once: no summary
	c.Event(ctx, write("p1", "/b", "t9"))
	// another process: its own run
	c.Event(ctx, write("p2", "/a", "t5"))
	// not a write: untouched
	c.Event(ctx, store.KernelEvent{ExecID: "p1", Type: "file_create", Path: "/c"})
	if len(out.log) != 4 {
		t.Fatalf("before exit: %v", out.log)
	}
	if err := c.Exit(ctx, "p1", time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	// p1's /a ran five times: one summary with the last time, then the exit
	last := out.evts[len(out.evts)-1]
	if last.ExecID != "p1" || last.Path != "/a" || last.TS != "t4" || last.Payload != `{"coalesced":true,"count":5}` {
		t.Errorf("summary: %+v", last)
	}
	if out.log[len(out.log)-1] != "exit p1" {
		t.Errorf("log: %v", out.log)
	}
	// p1's /b (once) made no summary, and p2's run is still pending
	if len(out.evts) != 5 {
		t.Errorf("%d events: %v", len(out.evts), out.log)
	}
	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(out.evts) != 5 { // p2 wrote once: nothing to report
		t.Errorf("flush made an event: %v", out.log)
	}
}

func TestCoalesceFlushesBeforeAGap(t *testing.T) {
	out := &logSink{}
	c := &CoalesceSink{Sink: out}
	ctx := context.Background()
	c.Event(ctx, write("p1", "/a", "t1"))
	c.Event(ctx, write("p1", "/a", "t2"))
	if _, err := c.GapOpened(ctx, time.Now(), "x"); err != nil {
		t.Fatal(err)
	}
	if got := out.log[len(out.log)-2:]; got[0] != `file_write /a {"coalesced":true,"count":2}` || got[1] != "gap" {
		t.Errorf("log: %v", out.log)
	}
	// counting starts again after the gap: the next write is a first write
	n := len(out.log)
	c.Event(ctx, write("p1", "/a", "t3"))
	if len(out.log) != n+1 {
		t.Errorf("log: %v", out.log)
	}
}

func TestCoalesceCompleteKeepsEveryWrite(t *testing.T) {
	out := &logSink{}
	c := &CoalesceSink{Sink: out, Level: policy.Complete}
	for i := 0; i < 4; i++ {
		c.Event(context.Background(), write("p1", "/a", "t"))
	}
	if len(out.evts) != 4 {
		t.Errorf("%d events at complete, want 4", len(out.evts))
	}
}

func TestCoalesceBoundsItsMemory(t *testing.T) {
	out := &logSink{}
	c := &CoalesceSink{Sink: out}
	ctx := context.Background()
	c.Event(ctx, write("p1", "/hot", "t1"))
	c.Event(ctx, write("p1", "/hot", "t2")) // a pending count
	for i := 0; i < maxWriteRuns; i++ {
		c.Event(ctx, write("p1", fmt.Sprintf("/f%d", i), "t"))
	}
	if len(c.runs) > maxWriteRuns {
		t.Errorf("%d runs kept", len(c.runs))
	}
	found := false
	for _, e := range out.evts {
		found = found || (e.Path == "/hot" && e.Payload == `{"coalesced":true,"count":2}`)
	}
	if !found {
		t.Error("the pending count was lost when the table filled")
	}
}

func TestCoalesceSinkErrors(t *testing.T) {
	boom := errors.New("disk full")
	ctx := context.Background()
	out := &logSink{}
	c := &CoalesceSink{Sink: out}
	c.Event(ctx, write("p1", "/a", "t1"))
	c.Event(ctx, write("p1", "/a", "t2"))
	out.fail = boom
	if err := c.Exit(ctx, "p1", time.Now(), 0); !errors.Is(err, boom) {
		t.Errorf("exit: %v", err)
	}
	c.Event(ctx, write("p2", "/a", "t1"))
	if err := c.Event(ctx, write("p3", "/z", "t")); !errors.Is(err, boom) {
		t.Errorf("event: %v", err)
	}
	c.runs = map[writeKey]*writeRun{{"p9", "/a"}: {count: 3, last: "t"}}
	if _, err := c.GapOpened(ctx, time.Now(), "x"); !errors.Is(err, boom) {
		t.Errorf("gap: %v", err)
	}
	c.runs = map[writeKey]*writeRun{{"p9", "/a"}: {count: 3, last: "t"}}
	if err := c.Flush(ctx); !errors.Is(err, boom) {
		t.Errorf("flush: %v", err)
	}
}

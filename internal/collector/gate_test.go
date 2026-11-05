package collector

import (
	"context"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/store"
)

// callRecorder counts what reached it.
type callRecorder struct {
	processes, exits, events, opened, closed int
}

func (c *callRecorder) Process(context.Context, store.Process) error { c.processes++; return nil }
func (c *callRecorder) Exit(context.Context, string, time.Time, int) error {
	c.exits++
	return nil
}
func (c *callRecorder) Event(context.Context, store.KernelEvent) error { c.events++; return nil }
func (c *callRecorder) GapOpened(context.Context, time.Time, string) (int64, error) {
	c.opened++
	return 1, nil
}
func (c *callRecorder) GapClosed(context.Context, int64, time.Time) error { c.closed++; return nil }

func TestGateDropsEverythingWhilePausedButNotGaps(t *testing.T) {
	ctx := context.Background()
	rec := &callRecorder{}
	paused := false
	g := &GateSink{Sink: rec, Paused: func() bool { return paused }}

	send := func() {
		g.Process(ctx, store.Process{})
		g.Exit(ctx, "e", time.Now(), 0)
		g.Event(ctx, store.KernelEvent{Type: "exec"})
		id, _ := g.GapOpened(ctx, time.Now(), "tetragon down")
		g.GapClosed(ctx, id, time.Now())
	}
	send()
	if *rec != (callRecorder{1, 1, 1, 1, 1}) {
		t.Errorf("running: %+v", *rec)
	}
	paused = true
	send()
	if *rec != (callRecorder{1, 1, 1, 2, 2}) { // only the collector's own gaps pass
		t.Errorf("paused: %+v", *rec)
	}
	paused = false
	send()
	if *rec != (callRecorder{2, 2, 2, 3, 3}) {
		t.Errorf("resumed: %+v", *rec)
	}
	// no Paused function: never paused
	g = &GateSink{Sink: rec}
	g.Event(ctx, store.KernelEvent{})
	if rec.events != 3 {
		t.Errorf("without a Paused func: %+v", *rec)
	}
}

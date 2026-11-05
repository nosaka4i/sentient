package collector

import (
	"context"
	"time"

	"github.com/nosaka4i/sentient/internal/store"
)

// GateSink drops what a collector reports while recording is paused (the disk
// is too full, docs/capture-verbosity.md). The kernel does not wait for us, so
// those events are gone; the pause is recorded as a gap by whoever paused.
// Gaps the collector itself reports still pass, so a Tetragon outage during a
// pause is not hidden.
type GateSink struct {
	Sink
	Paused func() bool
}

func (g *GateSink) paused() bool { return g.Paused != nil && g.Paused() }

// Process implements Sink.
func (g *GateSink) Process(ctx context.Context, p store.Process) error {
	if g.paused() {
		return nil
	}
	return g.Sink.Process(ctx, p)
}

// Exit implements Sink.
func (g *GateSink) Exit(ctx context.Context, execID string, at time.Time, code int) error {
	if g.paused() {
		return nil
	}
	return g.Sink.Exit(ctx, execID, at, code)
}

// Event implements Sink.
func (g *GateSink) Event(ctx context.Context, e store.KernelEvent) error {
	if g.paused() {
		return nil
	}
	return g.Sink.Event(ctx, e)
}

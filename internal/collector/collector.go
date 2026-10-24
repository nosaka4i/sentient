// Package collector is where kernel-level evidence comes from: what an
// agent's processes actually did, independent of the agent's own log. A
// Collector is one platform's source (Tetragon on Linux, eslogger on macOS,
// later the native Endpoint Security extension); it is chosen at runtime and
// hands the same OS-neutral records to a Sink, so the store, queries and
// pages never know which one produced a row. See
// docs/tetragon-integration.md.
package collector

import (
	"context"
	"time"

	"github.com/nosaka4i/sentient/internal/store"
)

// Collector watches the system and reports to a Sink until ctx is done.
type Collector interface {
	// Name is the collector's identifier as stored: "tetragon", "eslogger".
	Name() string
	// Run reports processes, events and gaps to sink. It returns when ctx is
	// cancelled or the source fails for good; temporary failures (the
	// subprocess died, the stream dropped) are reported as gaps and retried.
	Run(ctx context.Context, sink Sink) error
}

// Sink receives what a collector sees. Methods are called from one
// goroutine at a time, in the order things happened.
type Sink interface {
	// Process reports a process that started, or one already running when
	// the collector began.
	Process(ctx context.Context, p store.Process) error
	// Exit reports a process ending.
	Exit(ctx context.Context, execID string, at time.Time, code int) error
	// Event reports something a process did.
	Event(ctx context.Context, e store.KernelEvent) error
	// GapOpened reports that the collector stopped watching, and returns an
	// id to close it with.
	GapOpened(ctx context.Context, at time.Time, reason string) (int64, error)
	// GapClosed reports that the collector is watching again.
	GapClosed(ctx context.Context, id int64, at time.Time) error
}

package collector

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/nosaka4i/sentient/internal/collector/tetragon/policy"
	"github.com/nosaka4i/sentient/internal/store"
)

// maxWriteRuns bounds the writes a CoalesceSink remembers; past it, it reports
// what it has and starts over, so a process that writes a great many different
// files cannot grow it without limit.
const maxWriteRuns = 50000

// CoalesceSink collapses repeated writes by one process to one path into a
// single record, below the `complete` level (docs/mac-collector-design.md,
// open question 3). A program writing a file in many small pieces would
// otherwise store thousands of events for one change: a cold `go test ./...`
// made 146,069 writes to 6,880 distinct (process, path) pairs.
//
// The first write passes through at once, so the time of the change is its
// own. Later writes to the same path by the same process are counted, and the
// count is reported as one more file_write event, with the time of the last
// write and {"count":N,"coalesced":true}, when the process exits, when the
// collector reports a gap, and on Flush. At `complete` nothing is collapsed.
type CoalesceSink struct {
	Sink
	Level policy.Verbosity

	mu   sync.Mutex
	runs map[writeKey]*writeRun
}

type writeKey struct{ execID, path string }

type writeRun struct {
	count int64
	last  string // time of the latest write
}

// Event implements Sink.
func (c *CoalesceSink) Event(ctx context.Context, e store.KernelEvent) error {
	if e.Type != "file_write" || c.Level == policy.Complete {
		return c.Sink.Event(ctx, e)
	}
	k := writeKey{e.ExecID, e.Path}
	c.mu.Lock()
	if run, ok := c.runs[k]; ok {
		run.count++
		run.last = e.TS
		c.mu.Unlock()
		return nil
	}
	if c.runs == nil {
		c.runs = map[writeKey]*writeRun{}
	}
	var spill []store.KernelEvent
	if len(c.runs) >= maxWriteRuns {
		spill = c.takeLocked(func(writeKey) bool { return true })
	}
	c.runs[k] = &writeRun{count: 1, last: e.TS}
	c.mu.Unlock()
	if err := c.emit(ctx, spill); err != nil {
		return err
	}
	return c.Sink.Event(ctx, e)
}

// Exit implements Sink: the process's counts are reported before it ends.
func (c *CoalesceSink) Exit(ctx context.Context, execID string, at time.Time, code int) error {
	c.mu.Lock()
	summaries := c.takeLocked(func(k writeKey) bool { return k.execID == execID })
	c.mu.Unlock()
	if err := c.emit(ctx, summaries); err != nil {
		return err
	}
	return c.Sink.Exit(ctx, execID, at, code)
}

// GapOpened implements Sink: what was counted before the gap is reported
// before it, and the counting starts again after.
func (c *CoalesceSink) GapOpened(ctx context.Context, at time.Time, reason string) (int64, error) {
	if err := c.Flush(ctx); err != nil {
		return 0, err
	}
	return c.Sink.GapOpened(ctx, at, reason)
}

// Flush reports every pending count and forgets them.
func (c *CoalesceSink) Flush(ctx context.Context) error {
	c.mu.Lock()
	summaries := c.takeLocked(func(writeKey) bool { return true })
	c.mu.Unlock()
	return c.emit(ctx, summaries)
}

// takeLocked removes the runs that match and returns the summary events for
// the ones written more than once, in a stable order. c.mu must be held.
func (c *CoalesceSink) takeLocked(match func(writeKey) bool) []store.KernelEvent {
	var keys []writeKey
	for k := range c.runs {
		if match(k) {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].execID != keys[j].execID {
			return keys[i].execID < keys[j].execID
		}
		return keys[i].path < keys[j].path
	})
	var out []store.KernelEvent
	for _, k := range keys {
		run := c.runs[k]
		delete(c.runs, k)
		if run.count > 1 {
			payload, _ := json.Marshal(map[string]any{"count": run.count, "coalesced": true})
			out = append(out, store.KernelEvent{ExecID: k.execID, TS: run.last, Type: "file_write", Path: k.path, Payload: string(payload)})
		}
	}
	return out
}

func (c *CoalesceSink) emit(ctx context.Context, events []store.KernelEvent) error {
	for _, e := range events {
		if err := c.Sink.Event(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

package collector

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/store"
)

// BufferedSink is a Sink that queues what a collector reports and writes it
// to the store in batches, one transaction per batch, from one goroutine.
//
// The store has a single connection that the ingester and the API share, so a
// write can wait behind a long import. Writing one transaction per event would
// then fall behind a busy kernel stream, back up into the source and lose
// events there without anyone noticing. Here a slow store fills this queue
// instead; if the queue itself fills, events are dropped and the loss is
// recorded as a monitor gap.
type BufferedSink struct {
	direct *StoreSink
	ch     chan op

	dropped   atomic.Int64
	firstDrop atomic.Int64 // unix nanoseconds of the first drop since the last gap
}

type opKind int

const (
	opProcess opKind = iota
	opExit
	opEvent
)

type op struct {
	kind   opKind
	proc   store.Process
	event  store.KernelEvent
	execID string
	at     time.Time
	code   int
}

// batchMax is the most operations written in one transaction.
const batchMax = 500

// NewBufferedSink returns a sink that queues up to size operations. Run must
// be started for anything to reach the store.
func NewBufferedSink(st *store.Store, collector string, size int) *BufferedSink {
	return &BufferedSink{direct: &StoreSink{Store: st, Collector: collector}, ch: make(chan op, size)}
}

func (b *BufferedSink) enqueue(o op) {
	select {
	case b.ch <- o:
	default:
		if b.dropped.Add(1) == 1 {
			b.firstDrop.Store(time.Now().UnixNano())
		}
	}
}

// Process implements Sink.
func (b *BufferedSink) Process(_ context.Context, p store.Process) error {
	b.enqueue(op{kind: opProcess, proc: p})
	return nil
}

// Exit implements Sink.
func (b *BufferedSink) Exit(_ context.Context, execID string, at time.Time, code int) error {
	b.enqueue(op{kind: opExit, execID: execID, at: at, code: code})
	return nil
}

// Event implements Sink.
func (b *BufferedSink) Event(_ context.Context, e store.KernelEvent) error {
	b.enqueue(op{kind: opEvent, event: e})
	return nil
}

// GapOpened implements Sink. Gaps are written at once, not queued: the caller
// needs the id.
func (b *BufferedSink) GapOpened(ctx context.Context, at time.Time, reason string) (int64, error) {
	return b.direct.GapOpened(ctx, at, reason)
}

// GapClosed implements Sink.
func (b *BufferedSink) GapClosed(ctx context.Context, id int64, at time.Time) error {
	return b.direct.GapClosed(ctx, id, at)
}

// Dropped is how many operations have been dropped and not yet recorded as a
// gap.
func (b *BufferedSink) Dropped() int64 { return b.dropped.Load() }

// Run writes the queue to the store until ctx is done, then writes what is
// left. It returns the first store error.
func (b *BufferedSink) Run(ctx context.Context) error {
	// A batch that has been taken off the queue is always written, even when
	// ctx is cancelled meanwhile: stopping must not throw it away.
	wctx := context.WithoutCancel(ctx)
	for {
		var first op
		select {
		case <-ctx.Done():
			return b.flush(wctx)
		case first = <-b.ch:
		}
		batch := append(make([]op, 0, batchMax), first)
	fill:
		for len(batch) < batchMax {
			select {
			case o := <-b.ch:
				batch = append(batch, o)
			default:
				break fill
			}
		}
		if err := b.write(wctx, batch); err != nil {
			return err
		}
		if err := b.recordDrops(wctx); err != nil {
			return err
		}
	}
}

// flush writes everything still queued.
func (b *BufferedSink) flush(ctx context.Context) error {
	for {
		batch := make([]op, 0, batchMax)
	fill:
		for len(batch) < batchMax {
			select {
			case o := <-b.ch:
				batch = append(batch, o)
			default:
				break fill
			}
		}
		if len(batch) == 0 {
			return b.recordDrops(ctx)
		}
		if err := b.write(ctx, batch); err != nil {
			return err
		}
	}
}

func (b *BufferedSink) write(ctx context.Context, batch []op) error {
	return b.direct.do(ctx, func(tx *store.Tx) error {
		for _, o := range batch {
			var err error
			switch o.kind {
			case opProcess:
				err = tx.AddProcess(ctx, o.proc)
			case opExit:
				_, err = tx.ExitProcess(ctx, o.execID, dbtime.Format(o.at), o.code)
			case opEvent:
				_, err = tx.AppendKernel(ctx, o.event)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// recordDrops turns dropped operations into a monitor gap from the first drop
// until now, the moment the queue is draining again.
func (b *BufferedSink) recordDrops(ctx context.Context) error {
	n := b.dropped.Swap(0)
	if n == 0 {
		return nil
	}
	from := time.Unix(0, b.firstDrop.Load())
	id, err := b.direct.GapOpened(ctx, from, fmt.Sprintf("sentient could not keep up: dropped %d kernel records", n))
	if err != nil {
		return err
	}
	return b.direct.GapClosed(ctx, id, time.Now())
}

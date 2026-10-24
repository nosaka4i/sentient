package collector

import (
	"context"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/store"
)

// StoreSink is a Sink that writes everything to the store, one transaction
// per call. Attribution to sessions and prompts is a later step; this only
// records what the collector saw.
type StoreSink struct {
	Store     *store.Store
	Collector string // recorded with gaps ("eslogger")
}

func (s *StoreSink) do(ctx context.Context, f func(*store.Tx) error) error {
	tx, err := s.Store.Begin(ctx)
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Process implements Sink.
func (s *StoreSink) Process(ctx context.Context, p store.Process) error {
	return s.do(ctx, func(tx *store.Tx) error { return tx.AddProcess(ctx, p) })
}

// Exit implements Sink.
func (s *StoreSink) Exit(ctx context.Context, execID string, at time.Time, code int) error {
	return s.do(ctx, func(tx *store.Tx) error {
		_, err := tx.ExitProcess(ctx, execID, dbtime.Format(at), code)
		return err
	})
}

// Event implements Sink.
func (s *StoreSink) Event(ctx context.Context, e store.KernelEvent) error {
	return s.do(ctx, func(tx *store.Tx) error {
		_, err := tx.AppendKernel(ctx, e)
		return err
	})
}

// GapOpened implements Sink.
func (s *StoreSink) GapOpened(ctx context.Context, at time.Time, reason string) (int64, error) {
	var id int64
	err := s.do(ctx, func(tx *store.Tx) (err error) {
		id, err = tx.OpenGap(ctx, store.Gap{Collector: s.Collector, StartedAt: dbtime.Format(at), Reason: reason})
		return err
	})
	return id, err
}

// GapClosed implements Sink.
func (s *StoreSink) GapClosed(ctx context.Context, id int64, at time.Time) error {
	return s.do(ctx, func(tx *store.Tx) error {
		_, err := tx.CloseGap(ctx, id, dbtime.Format(at))
		return err
	})
}

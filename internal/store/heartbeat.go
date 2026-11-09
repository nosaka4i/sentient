package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Heartbeat is when sentientd last saw a collector's kernel being watched, and
// whether it then stopped on purpose (docs/heartbeat.md).
type Heartbeat struct {
	SeenAt string
	Clean  bool
}

// Heartbeat returns the collector's row; ok is false when there is none (a
// first run).
func (s *Store) Heartbeat(ctx context.Context, collector string) (h Heartbeat, ok bool, err error) {
	var clean int
	err = s.DB.QueryRowContext(ctx, `SELECT seen_at, clean FROM heartbeat WHERE collector = ?`, collector).Scan(&h.SeenAt, &clean)
	if errors.Is(err, sql.ErrNoRows) {
		return Heartbeat{}, false, nil
	}
	if err != nil {
		return Heartbeat{}, false, fmt.Errorf("read heartbeat: %w", err)
	}
	h.Clean = clean != 0
	return h, true, nil
}

// SetHeartbeat records that the collector's kernel was being watched at seenAt.
// clean marks a stop on purpose; every other write passes false, so a row that
// is still not clean at the next start means the last run did not end properly.
func (s *Store) SetHeartbeat(ctx context.Context, collector, seenAt string, clean bool) error {
	c := 0
	if clean {
		c = 1
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO heartbeat (collector, seen_at, clean) VALUES (?, ?, ?)
		ON CONFLICT(collector) DO UPDATE SET seen_at = excluded.seen_at, clean = excluded.clean`, collector, seenAt, c)
	if err != nil {
		return fmt.Errorf("write heartbeat: %w", err)
	}
	return nil
}

// RecordClosedGap writes a gap that has already ended, in one transaction.
func (s *Store) RecordClosedGap(ctx context.Context, g Gap, endedAt string) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id, err := tx.OpenGap(ctx, g)
	if err != nil {
		return err
	}
	if _, err := tx.CloseGap(ctx, id, endedAt); err != nil {
		return err
	}
	return tx.Commit()
}

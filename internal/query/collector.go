package query

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/health"
)

// CollectorStats fills in what the database holds of the kernel collector's
// work: events and processes stored, and the gaps in its coverage.
func CollectorStats(ctx context.Context, db *sql.DB, c *health.Collector) error {
	var first sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(ts) FROM kernel_events`).Scan(&c.Stored, &first); err != nil {
		return fmt.Errorf("kernel events: %w", err)
	}
	if first.Valid {
		t, err := dbtime.Parse(first.String)
		if err != nil {
			return err
		}
		c.FirstEvent = t
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM processes`).Scan(&c.Processes); err != nil {
		return fmt.Errorf("processes: %w", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM process_attribution`).Scan(&c.AgentProcesses); err != nil {
		return fmt.Errorf("attributed processes: %w", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM monitor_gaps`).Scan(&c.Gaps); err != nil {
		return fmt.Errorf("gaps: %w", err)
	}
	var from string
	var to sql.NullString
	var reason string
	err := db.QueryRowContext(ctx, `SELECT started_at, ended_at, reason FROM monitor_gaps ORDER BY started_at DESC, id DESC LIMIT 1`).Scan(&from, &to, &reason)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("latest gap: %w", err)
	}
	f, err := dbtime.Parse(from)
	if err != nil {
		return err
	}
	c.Latest = health.GapNote{Set: true, From: f, Reason: reason, Open: !to.Valid}
	if to.Valid {
		if c.Latest.To, err = dbtime.Parse(to.String); err != nil {
			return err
		}
	}
	return nil
}

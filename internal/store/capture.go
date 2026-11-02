package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CaptureLevel is one row of capture_levels: the level sentientd recorded at
// from ts until the next row.
type CaptureLevel struct {
	TS     string
	Level  string
	Source string // flag | before-setting
}

// BeforeSetting is the level a database recorded at before the setting
// existed: everything Tetragon reported.
const BeforeSetting = "complete"

// RecordCaptureLevel appends the level sentientd starts with, and returns the
// level before it ("" on a fresh database). A database that already holds
// kernel events but no level row was recorded before the setting existed, so
// that is recorded first as complete, source before-setting, at the time of
// its first event. Nothing already stored is touched.
func (s *Store) RecordCaptureLevel(ctx context.Context, ts, level, source string) (previous string, err error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	err = tx.tx.QueryRowContext(ctx, `SELECT level FROM capture_levels ORDER BY id DESC LIMIT 1`).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		previous = ""
		var first sql.NullString
		if err := tx.tx.QueryRowContext(ctx, `SELECT MIN(ts) FROM kernel_events`).Scan(&first); err != nil {
			return "", fmt.Errorf("first kernel event: %w", err)
		}
		if first.Valid {
			if _, err := tx.tx.ExecContext(ctx, `INSERT INTO capture_levels (ts, level, source) VALUES (?, ?, 'before-setting')`, first.String, BeforeSetting); err != nil {
				return "", fmt.Errorf("record earlier level: %w", err)
			}
			previous = BeforeSetting
		}
	} else if err != nil {
		return "", fmt.Errorf("read capture level: %w", err)
	}
	if _, err := tx.tx.ExecContext(ctx, `INSERT INTO capture_levels (ts, level, source) VALUES (?, ?, ?)`, ts, level, source); err != nil {
		return "", fmt.Errorf("record capture level: %w", err)
	}
	return previous, tx.Commit()
}

// CaptureLevels lists the rows in the order they were recorded.
func (s *Store) CaptureLevels(ctx context.Context) ([]CaptureLevel, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT ts, level, source FROM capture_levels ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("capture levels: %w", err)
	}
	defer rows.Close()
	var out []CaptureLevel
	for rows.Next() {
		var c CaptureLevel
		if err := rows.Scan(&c.TS, &c.Level, &c.Source); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

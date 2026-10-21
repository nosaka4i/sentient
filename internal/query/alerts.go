package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
)

// Alert statuses and severities, as stored.
const (
	AlertOpen     = "open"
	AlertReviewed = "reviewed"
)

// MaxListedAlerts is the most alerts one list request returns.
const MaxListedAlerts = 2000

// AlertRow is an alert with the action, prompt and session it came from.
type AlertRow struct {
	ID                     int64
	At                     time.Time
	Severity, Rule, Status string
	ReviewedAt             time.Time
	ActionID               int64 // 0 for an alert with no action (a monitor gap)
	Resource, Action, Path string
	Summary                string
	PromptID               int64
	PromptText             string
	Agent, Repo, SessionID string
}

// AlertCounts is how many alerts there are by status and severity.
type AlertCounts struct {
	Open, Reviewed                int64
	OpenHigh, OpenMedium, OpenLow int64
}

// AlertList is the alerts page's data: matching alerts newest first, and
// counts over all alerts.
type AlertList struct {
	HistoryBegins time.Time
	Counts        AlertCounts
	Alerts        []AlertRow
}

// AlertFilter narrows a list; empty fields match everything.
type AlertFilter struct {
	Status   string // open | reviewed
	Severity string // high | medium | low
	Limit    int    // 0 = MaxListedAlerts
}

const alertSelect = `
SELECT al.id, al.ts, al.severity, al.rule, al.status, al.reviewed_at,
       COALESCE(a.id, 0), COALESCE(a.resource, ''), COALESCE(a.action, ''), COALESCE(a.path, ''), COALESCE(a.summary, ''),
       COALESCE(a.prompt_id, 0), COALESCE(p.text, ''), COALESCE(s.agent, ''), COALESCE(s.repo_root, s.cwd, ''), COALESCE(s.agent_session_id, '')
FROM alerts al
LEFT JOIN actions a ON a.id = al.action_id
LEFT JOIN prompts p ON p.id = a.prompt_id
LEFT JOIN sessions s ON s.id = a.session_id`

func scanAlert(sc interface{ Scan(...any) error }) (AlertRow, error) {
	var r AlertRow
	var ts string
	var reviewed sql.NullString
	var dir string
	if err := sc.Scan(&r.ID, &ts, &r.Severity, &r.Rule, &r.Status, &reviewed,
		&r.ActionID, &r.Resource, &r.Action, &r.Path, &r.Summary, &r.PromptID, &r.PromptText, &r.Agent, &dir, &r.SessionID); err != nil {
		return r, err
	}
	var err error
	if r.At, err = dbtime.Parse(ts); err != nil {
		return r, err
	}
	if reviewed.Valid {
		if r.ReviewedAt, err = dbtime.Parse(reviewed.String); err != nil {
			return r, err
		}
	}
	if dir != "" {
		r.Repo = filepath.Base(dir)
	}
	return r, nil
}

// ListAlerts returns the alerts matching f, newest first, and the counts of
// all alerts.
func ListAlerts(ctx context.Context, db *sql.DB, f AlertFilter) (*AlertList, error) {
	l := &AlertList{}
	var err error
	if l.HistoryBegins, err = HistoryBegins(ctx, db); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT status, severity, COUNT(*) FROM alerts GROUP BY status, severity`)
	if err != nil {
		return nil, fmt.Errorf("alert counts: %w", err)
	}
	for rows.Next() {
		var status, sev string
		var n int64
		if err := rows.Scan(&status, &sev, &n); err != nil {
			rows.Close()
			return nil, err
		}
		switch {
		case status == AlertReviewed:
			l.Counts.Reviewed += n
		case status == AlertOpen:
			l.Counts.Open += n
			switch sev {
			case "high":
				l.Counts.OpenHigh += n
			case "medium":
				l.Counts.OpenMedium += n
			case "low":
				l.Counts.OpenLow += n
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	where, args := "1 = 1", []any{}
	if f.Status != "" {
		where += ` AND al.status = ?`
		args = append(args, f.Status)
	}
	if f.Severity != "" {
		where += ` AND al.severity = ?`
		args = append(args, f.Severity)
	}
	limit := f.Limit
	if limit <= 0 || limit > MaxListedAlerts {
		limit = MaxListedAlerts
	}
	r, err := db.QueryContext(ctx, alertSelect+` WHERE `+where+` ORDER BY al.ts DESC, al.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	defer r.Close()
	for r.Next() {
		a, err := scanAlert(r)
		if err != nil {
			return nil, err
		}
		l.Alerts = append(l.Alerts, a)
	}
	return l, r.Err()
}

// AlertDetail is one alert and the agent's own record of the action behind it.
type AlertDetail struct {
	HistoryBegins time.Time
	Alert         AlertRow
	RawRecord     string // the agent's log record for the action, capped; "" if none
}

// maxRawRecord caps the raw record shown with an alert.
const maxRawRecord = 12000

// GetAlert returns one alert, or ErrNotFound.
func GetAlert(ctx context.Context, db *sql.DB, id int64) (*AlertDetail, error) {
	d := &AlertDetail{}
	var err error
	if d.HistoryBegins, err = HistoryBegins(ctx, db); err != nil {
		return nil, err
	}
	d.Alert, err = scanAlert(db.QueryRowContext(ctx, alertSelect+` WHERE al.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("alert: %w", err)
	}
	if d.Alert.ActionID != 0 {
		var rec sql.NullString
		err := db.QueryRowContext(ctx, `
SELECT r.record FROM actions a JOIN raw_log_records r ON r.id = a.raw_record_id WHERE a.id = ?`, d.Alert.ActionID).Scan(&rec)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("alert record: %w", err)
		}
		if rec.Valid {
			d.RawRecord = Ellipsis(rec.String, maxRawRecord)
		}
	}
	return d, nil
}

// ReviewAlert marks an alert reviewed (or open again), or returns
// ErrNotFound. Reviewing an already reviewed alert keeps its first time.
func ReviewAlert(ctx context.Context, db *sql.DB, id int64, reviewed bool, now time.Time) error {
	var res sql.Result
	var err error
	if reviewed {
		res, err = db.ExecContext(ctx, `UPDATE alerts SET status = 'reviewed', reviewed_at = COALESCE(reviewed_at, ?) WHERE id = ?`, dbtime.Format(now), id)
	} else {
		res, err = db.ExecContext(ctx, `UPDATE alerts SET status = 'open', reviewed_at = NULL WHERE id = ?`, id)
	}
	if err != nil {
		return fmt.Errorf("review alert: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

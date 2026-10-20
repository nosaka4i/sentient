package query

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
)

// MaxListedSessions is the most sessions one list request returns.
const MaxListedSessions = 1000

// SessionRow is a session in the Sessions list.
type SessionRow struct {
	SessionSummary
	FirstPrompt string // the session's first prompt, as the user typed it
	Running     bool
}

// SessionList is what the Sessions page shows: sessions active in a time
// window, most recently active first. The web layer filters and sorts it.
type SessionList struct {
	HistoryBegins time.Time
	Sessions      []SessionRow
	Total         int64 // sessions with prompts, in any window
	Agents        []string
	Repos         []string
}

// ListSessions returns the sessions active in the last days days (0 = all
// time), newest activity first. now anchors the window and decides which
// session still counts as running.
func ListSessions(ctx context.Context, db *sql.DB, days int, now time.Time) (*SessionList, error) {
	l := &SessionList{}
	var err error
	if l.HistoryBegins, err = HistoryBegins(ctx, db); err != nil {
		return nil, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions s WHERE `+hasPrompts).Scan(&l.Total); err != nil {
		return nil, fmt.Errorf("session count: %w", err)
	}

	lastActive := `COALESCE(s.ended_at, s.started_at, '')`
	where, args := hasPrompts, []any{dbtime.Format(now.Add(-RunningWindow))}
	if days > 0 {
		where += ` AND ` + lastActive + ` >= ?`
		args = append(args, dbtime.Format(now.Add(-time.Duration(days)*24*time.Hour)))
	}
	args = append(args, MaxListedSessions)
	r, err := db.QueryContext(ctx, `
SELECT s.agent_session_id, s.agent, `+repoOf+`, s.started_at, s.ended_at,
  (SELECT COUNT(*) FROM prompts p WHERE p.session_id = s.id),
  (SELECT COALESCE(SUM(p.action_count), 0) FROM prompts p WHERE p.session_id = s.id),
  (SELECT COUNT(*) FROM alerts al JOIN actions a ON a.id = al.action_id WHERE a.session_id = s.id AND al.status = 'open'),
  COALESCE((SELECT p.text FROM prompts p WHERE p.session_id = s.id ORDER BY p.seq LIMIT 1), ''),
  EXISTS (SELECT 1 FROM prompts p WHERE p.session_id = s.id AND p.ended_at IS NULL AND p.last_activity_at >= ?)
FROM sessions s WHERE `+where+`
ORDER BY `+lastActive+` DESC, s.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var row SessionRow
		var dir string
		var started, ended sql.NullString
		if err := r.Scan(&row.ID, &row.Agent, &dir, &started, &ended, &row.Prompts, &row.Actions, &row.Flags, &row.FirstPrompt, &row.Running); err != nil {
			return nil, err
		}
		if dir != "" {
			row.Repo = filepath.Base(dir)
		}
		if started.Valid {
			if row.Started, err = dbtime.Parse(started.String); err != nil {
				return nil, err
			}
		}
		if ended.Valid {
			if row.LastActivity, err = dbtime.Parse(ended.String); err != nil {
				return nil, err
			}
		}
		l.Sessions = append(l.Sessions, row)
	}
	if err := r.Err(); err != nil {
		return nil, err
	}
	if l.Agents, l.Repos, err = filterOptions(ctx, db); err != nil {
		return nil, err
	}
	return l, nil
}

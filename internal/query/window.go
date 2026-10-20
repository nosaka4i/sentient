package query

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
)

// Bounds of a prompt window.
const (
	// MaxWindowPrompts is the most prompts one window request returns; older
	// ones are reached by asking again with Before.
	MaxWindowPrompts = 5000
	// DefaultMinWindowPrompts widens a quiet window to at least this many.
	DefaultMinWindowPrompts = 100
)

// WindowRequest asks for a time window of prompts or one session's prompts.
type WindowRequest struct {
	Days       int       // the last N days; 0 = all time
	Before     time.Time // only prompts that started before this (paging older)
	SessionID  string    // one session's prompts instead of a time window
	Limit      int       // 0 = MaxWindowPrompts
	MinPrompts int       // a window with fewer is widened to this many; 0 = DefaultMinWindowPrompts
}

// PromptWindow is what the prompts page loads: a slice of prompts, newest
// first, with the sessions they belong to. The browser filters, sorts and
// groups it.
type PromptWindow struct {
	HistoryBegins time.Time
	Prompts       []PromptRow
	Sessions      []SessionSummary // the sessions of those prompts
	Matching      int64            // prompts in the window or session, before the limit
	More          bool             // older ones exist beyond these
	Oldest        time.Time        // when the oldest returned prompt started
	Widened       bool             // the window was widened to reach MinPrompts
	Agents        []string
	Repos         []string
	Menu          []SessionOption
	OlderSessions int64
}

// GetPromptWindow returns the window req describes. now anchors "the last
// N days" and decides which prompt still counts as running.
func GetPromptWindow(ctx context.Context, db *sql.DB, req WindowRequest, now time.Time) (*PromptWindow, error) {
	w := &PromptWindow{}
	var err error
	if w.HistoryBegins, err = HistoryBegins(ctx, db); err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit <= 0 || limit > MaxWindowPrompts {
		limit = MaxWindowPrompts
	}
	minPrompts := req.MinPrompts
	if minPrompts <= 0 {
		minPrompts = DefaultMinWindowPrompts
	}

	where, args := "1 = 1", []any{}
	switch {
	case req.SessionID != "":
		where += ` AND s.agent_session_id = ?`
		args = append(args, req.SessionID)
	case req.Days > 0:
		from := now.Add(-time.Duration(req.Days) * 24 * time.Hour)
		if req.Before.IsZero() {
			// A quiet window is widened to the newest minPrompts.
			var n int64
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM prompts WHERE started_at >= ?`, dbtime.Format(from)).Scan(&n); err != nil {
				return nil, fmt.Errorf("window size: %w", err)
			}
			if n < int64(minPrompts) {
				var edge sql.NullString
				if err := db.QueryRowContext(ctx, `SELECT started_at FROM prompts ORDER BY started_at DESC, id DESC LIMIT 1 OFFSET ?`, minPrompts-1).Scan(&edge); err != nil && err != sql.ErrNoRows {
					return nil, fmt.Errorf("window edge: %w", err)
				}
				w.Widened = true
				if edge.Valid {
					if t, err := dbtime.Parse(edge.String); err == nil && t.Before(from) {
						from = t
					}
				}
				if !edge.Valid {
					from = time.Time{} // fewer than minPrompts prompts exist: everything
				}
			}
		}
		if !from.IsZero() {
			where += ` AND p.started_at >= ?`
			args = append(args, dbtime.Format(from))
		}
	}
	if !req.Before.IsZero() {
		where += ` AND p.started_at < ?`
		args = append(args, dbtime.Format(req.Before))
	}

	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM prompts p JOIN sessions s ON s.id = p.session_id WHERE `+where, args...).Scan(&w.Matching); err != nil {
		return nil, fmt.Errorf("window total: %w", err)
	}
	r, err := db.QueryContext(ctx, `
SELECT p.id, p.text, p.started_at, p.ended_at, p.last_activity_at, p.action_count, `+openFlags+`,
       s.agent, `+repoOf+`, s.agent_session_id
FROM prompts p JOIN sessions s ON s.id = p.session_id
WHERE `+where+` ORDER BY p.started_at DESC, p.id DESC LIMIT ?`, append(append([]any(nil), args...), limit)...)
	if err != nil {
		return nil, fmt.Errorf("window prompts: %w", err)
	}
	defer r.Close()
	seen := map[string]bool{}
	var order []string
	for r.Next() {
		var p PromptRow
		var started string
		var ended, last sql.NullString
		var dir string
		if err := r.Scan(&p.ID, &p.Text, &started, &ended, &last, &p.Actions, &p.Flags, &p.Agent, &dir, &p.SessionID); err != nil {
			return nil, err
		}
		if p.Started, err = dbtime.Parse(started); err != nil {
			return nil, err
		}
		if dir != "" {
			p.Repo = filepath.Base(dir)
		}
		if last.Valid {
			lt, err := dbtime.Parse(last.String)
			if err != nil {
				return nil, err
			}
			p.Duration = max(lt.Sub(p.Started), 0)
			p.Running = !ended.Valid && now.Sub(lt) <= RunningWindow
		}
		if !seen[p.SessionID] {
			seen[p.SessionID] = true
			order = append(order, p.SessionID)
		}
		w.Prompts = append(w.Prompts, p)
	}
	if err := r.Err(); err != nil {
		return nil, err
	}
	r.Close()

	w.More = w.Matching > int64(len(w.Prompts))
	if n := len(w.Prompts); n > 0 {
		w.Oldest = w.Prompts[n-1].Started
	}
	for _, id := range order {
		sum, err := GetSessionSummary(ctx, db, id)
		if err != nil {
			return nil, err
		}
		w.Sessions = append(w.Sessions, *sum)
	}
	if w.Agents, w.Repos, err = filterOptions(ctx, db); err != nil {
		return nil, err
	}
	if w.Menu, w.OlderSessions, err = sessionOptions(ctx, db); err != nil {
		return nil, err
	}
	return w, nil
}

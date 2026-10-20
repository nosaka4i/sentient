package query

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Defaults and bounds for the grouped view.
const (
	DefaultSessionLimit = 10
	MaxSessionLimit     = 200
	DefaultPerSession   = 3
)

// SessionGroup is one session with the newest of its prompts matching a
// filter.
type SessionGroup struct {
	Session  SessionSummary // Prompts is the session's total
	Matching int64          // its prompts matching the filter
	Prompts  []PromptRow    // the newest few of those, newest first
}

// SessionGroups is the data behind the prompts page's default view: every
// session with a matching prompt, newest first, each with a few prompts.
type SessionGroups struct {
	HistoryBegins time.Time
	TotalPrompts  int64 // prompts matching the filter, all sessions
	TotalSessions int64 // sessions with a matching prompt
	Groups        []SessionGroup
	Agents        []string
	Repos         []string
	Sessions      []SessionOption
	OlderSessions int64
}

// ListSessionGroups returns up to f's session limit (f.Limit; 0 =
// DefaultSessionLimit) sessions that have a prompt matching f, ordered by
// their newest matching prompt, each with its newest perSession (0 =
// DefaultPerSession) matching prompts.
func ListSessionGroups(ctx context.Context, db *sql.DB, f PromptFilter, perSession int, now time.Time) (*SessionGroups, error) {
	g := &SessionGroups{}
	var err error
	if g.HistoryBegins, err = HistoryBegins(ctx, db); err != nil {
		return nil, err
	}
	f.SessionID = "" // the grouped view spans sessions
	cond, args := f.cond()
	from := ` FROM prompts p JOIN sessions s ON s.id = p.session_id WHERE ` + cond
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT p.session_id)`+from, args...).Scan(&g.TotalPrompts, &g.TotalSessions); err != nil {
		return nil, fmt.Errorf("group totals: %w", err)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = DefaultSessionLimit
	}
	limit = min(limit, MaxSessionLimit)
	if perSession <= 0 {
		perSession = DefaultPerSession
	}

	r, err := db.QueryContext(ctx, `SELECT p.session_id, s.agent_session_id, COUNT(*)`+from+`
GROUP BY p.session_id ORDER BY MAX(p.started_at) DESC, p.session_id DESC LIMIT ?`, append(append([]any(nil), args...), limit)...)
	if err != nil {
		return nil, fmt.Errorf("matching sessions: %w", err)
	}
	type hit struct {
		row      int64
		agentID  string
		matching int64
	}
	var hits []hit
	for r.Next() {
		var h hit
		if err := r.Scan(&h.row, &h.agentID, &h.matching); err != nil {
			r.Close()
			return nil, err
		}
		hits = append(hits, h)
	}
	r.Close()
	if err := r.Err(); err != nil {
		return nil, err
	}

	for _, h := range hits {
		sum, err := GetSessionSummary(ctx, db, h.agentID)
		if err != nil {
			return nil, err
		}
		sf := f
		sf.SessionID = h.agentID
		scond, sargs := sf.cond()
		prompts, err := loadPrompts(ctx, db, scond, sargs, `p.started_at DESC, p.id DESC LIMIT ?`, perSession, now)
		if err != nil {
			return nil, err
		}
		g.Groups = append(g.Groups, SessionGroup{Session: *sum, Matching: h.matching, Prompts: prompts})
	}
	if g.Agents, g.Repos, err = filterOptions(ctx, db); err != nil {
		return nil, err
	}
	if g.Sessions, g.OlderSessions, err = sessionOptions(ctx, db); err != nil {
		return nil, err
	}
	return g, nil
}

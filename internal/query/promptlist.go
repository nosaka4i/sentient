package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
)

// SessionMenuSize is how many recent sessions the Session menu offers.
const SessionMenuSize = 30

// SessionSummary is a session in a line: who, where, when, how much.
type SessionSummary struct {
	ID, Agent, Repo string
	Started         time.Time
	LastActivity    time.Time
	Prompts         int64 // all the session's prompts
	Actions         int64 // actions on the system
	Flags           int64
}

// SessionOption is one entry of the Session filter menu.
type SessionOption struct {
	ID, Agent, Repo string
}

// repoOf is the SQL expression for a session's repo path.
const repoOf = `COALESCE(s.repo_root, s.cwd, '')`

// filterOptions lists the agents and repos to offer in the filters.
func filterOptions(ctx context.Context, db *sql.DB) (agents, repos []string, err error) {
	r, err := db.QueryContext(ctx, `SELECT DISTINCT s.agent, `+repoOf+` FROM sessions s`)
	if err != nil {
		return nil, nil, fmt.Errorf("filter options: %w", err)
	}
	defer r.Close()
	a, rp := map[string]bool{}, map[string]bool{}
	for r.Next() {
		var agent, dir string
		if err := r.Scan(&agent, &dir); err != nil {
			return nil, nil, err
		}
		a[agent] = true
		if dir != "" {
			rp[filepath.Base(dir)] = true
		}
	}
	if err := r.Err(); err != nil {
		return nil, nil, err
	}
	return sortedKeys(a), sortedKeys(rp), nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// hasPrompts limits session queries to sessions that have prompts (some
// only hold log metadata).
const hasPrompts = `EXISTS (SELECT 1 FROM prompts p WHERE p.session_id = s.id)`

// sessionOptions returns the SessionMenuSize most recently active sessions
// and how many more there are.
func sessionOptions(ctx context.Context, db *sql.DB) ([]SessionOption, int64, error) {
	r, err := db.QueryContext(ctx, `
SELECT s.agent_session_id, s.agent, `+repoOf+` FROM sessions s
WHERE `+hasPrompts+`
ORDER BY COALESCE(s.ended_at, s.started_at, '') DESC, s.id DESC LIMIT ?`, SessionMenuSize)
	if err != nil {
		return nil, 0, fmt.Errorf("session menu: %w", err)
	}
	defer r.Close()
	var out []SessionOption
	for r.Next() {
		var o SessionOption
		var dir string
		if err := r.Scan(&o.ID, &o.Agent, &dir); err != nil {
			return nil, 0, err
		}
		if dir != "" {
			o.Repo = filepath.Base(dir)
		}
		out = append(out, o)
	}
	if err := r.Err(); err != nil {
		return nil, 0, err
	}
	var total int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions s WHERE `+hasPrompts).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("session count: %w", err)
	}
	return out, max(total-int64(len(out)), 0), nil
}

// GetSessionSummary summarizes the session with the agent's session id, or
// returns ErrNotFound.
func GetSessionSummary(ctx context.Context, db *sql.DB, agentSessionID string) (*SessionSummary, error) {
	var (
		id, started, ended = int64(0), sql.NullString{}, sql.NullString{}
		dir                string
	)
	s := &SessionSummary{}
	err := db.QueryRowContext(ctx, `
SELECT s.id, s.agent_session_id, s.agent, `+repoOf+`, s.started_at, s.ended_at
FROM sessions s WHERE s.agent_session_id = ? ORDER BY s.id LIMIT 1`, agentSessionID).
		Scan(&id, &s.ID, &s.Agent, &dir, &started, &ended)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("session summary: %w", err)
	}
	if dir != "" {
		s.Repo = filepath.Base(dir)
	}
	if started.Valid {
		if s.Started, err = dbtime.Parse(started.String); err != nil {
			return nil, err
		}
	}
	if ended.Valid {
		if s.LastActivity, err = dbtime.Parse(ended.String); err != nil {
			return nil, err
		}
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM prompts WHERE session_id = ?`, id).Scan(&s.Prompts); err != nil {
		return nil, fmt.Errorf("session prompts: %w", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM actions a WHERE a.session_id = ? AND `+systemAction, id).Scan(&s.Actions); err != nil {
		return nil, fmt.Errorf("session actions: %w", err)
	}
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM alerts al JOIN actions a ON a.id = al.action_id WHERE a.session_id = ? AND al.status = 'open'`, id).Scan(&s.Flags); err != nil {
		return nil, fmt.Errorf("session flags: %w", err)
	}
	return s, nil
}

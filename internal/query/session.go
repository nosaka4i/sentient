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

// SessionPromptPreview is how many of a session's newest prompts its page
// lists; the prompts page has the rest.
const SessionPromptPreview = 5

// SessionDetail is everything the session page shows.
type SessionDetail struct {
	HistoryBegins time.Time
	ID            string // the agent's session id
	Agent         string
	AgentVersion  string
	Repo          string // repo (or cwd) basename
	Cwd           string
	Started       time.Time
	LastActivity  time.Time
	Running       bool
	Actions       int64 // actions on the system, all prompts
	Flags         int64
	TotalPrompts  int64
	FirstPrompt   string      // the text the session started with
	Prompts       []PromptRow // the newest SessionPromptPreview, newest first
	// Activity holds exactly the rows of StatusRows, scoped to the session.
	Activity  []ActivityRow
	Subagents []Subagent
}

// Subagent is one subagent a session started.
type Subagent struct {
	Kind, Task   string
	At           time.Time
	PromptNumber int64 // 1-based position of the prompt that started it, 0 if unknown
}

// GetSessionDetail returns the session with the agent's session id, or
// ErrNotFound. now decides whether its latest prompt still counts as running.
func GetSessionDetail(ctx context.Context, db *sql.DB, agentSessionID string, now time.Time) (*SessionDetail, error) {
	d := &SessionDetail{}
	var err error
	if d.HistoryBegins, err = HistoryBegins(ctx, db); err != nil {
		return nil, err
	}

	var (
		id                    int64
		repoRoot, cwd         string
		started, ended, agver sql.NullString
	)
	err = db.QueryRowContext(ctx, `
SELECT id, agent, agent_session_id, COALESCE(repo_root, ''), COALESCE(cwd, ''), started_at, ended_at, agent_version
FROM sessions WHERE agent_session_id = ? ORDER BY id LIMIT 1`, agentSessionID).
		Scan(&id, &d.Agent, &d.ID, &repoRoot, &cwd, &started, &ended, &agver)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	d.Cwd = cwd
	d.AgentVersion = agver.String
	switch {
	case repoRoot != "":
		d.Repo = filepath.Base(repoRoot)
	case cwd != "":
		d.Repo = filepath.Base(cwd)
	}
	if started.Valid {
		if d.Started, err = dbtime.Parse(started.String); err != nil {
			return nil, err
		}
	}
	if ended.Valid {
		if d.LastActivity, err = dbtime.Parse(ended.String); err != nil {
			return nil, err
		}
	}

	// Totals cover the whole session; only the newest few prompts are loaded
	// (the prompts page lists them all).
	sum, err := GetSessionSummary(ctx, db, agentSessionID)
	if err != nil {
		return nil, err
	}
	d.TotalPrompts, d.Actions, d.Flags = sum.Prompts, sum.Actions, sum.Flags
	if err := db.QueryRowContext(ctx, `SELECT COALESCE((SELECT text FROM prompts WHERE session_id = ? ORDER BY seq LIMIT 1), '')`, id).Scan(&d.FirstPrompt); err != nil {
		return nil, fmt.Errorf("first prompt: %w", err)
	}
	if d.Prompts, err = loadPrompts(ctx, db, `p.session_id = ?`, []any{id}, `p.seq DESC LIMIT ?`, SessionPromptPreview, now); err != nil {
		return nil, err
	}
	if len(d.Prompts) > 0 {
		d.Running = d.Prompts[0].Running
	}
	if d.Started.IsZero() {
		d.Started = sum.Started
	}
	if d.Started.IsZero() && d.TotalPrompts > 0 {
		var first string
		if err := db.QueryRowContext(ctx, `SELECT MIN(started_at) FROM prompts WHERE session_id = ?`, id).Scan(&first); err != nil {
			return nil, fmt.Errorf("session start: %w", err)
		}
		if d.Started, err = dbtime.Parse(first); err != nil {
			return nil, err
		}
	}

	rows := map[string]*ActivityRow{}
	for _, r := range StatusRows {
		rows[r[0]+" "+r[1]] = &ActivityRow{Resource: r[0], Action: r[1]}
	}
	pr := rows["prompt request"]
	pr.Count = d.TotalPrompts
	if d.Repo != "" && pr.Count > 0 {
		pr.Repos, pr.Summary = 1, "1 repo"
	}
	if err := actionRows(ctx, db, `a.session_id = ?`, []any{id}, rows); err != nil {
		return nil, err
	}
	for _, r := range StatusRows {
		d.Activity = append(d.Activity, *rows[r[0]+" "+r[1]])
	}

	sr, err := db.QueryContext(ctx, `
SELECT a.ts, COALESCE(a.path, ''), COALESCE(a.summary, ''), COALESCE(p.seq, 0)
FROM actions a LEFT JOIN prompts p ON p.id = a.prompt_id
WHERE a.session_id = ? AND a.resource = 'subagent' AND a.action = 'start' ORDER BY a.ts, a.id`, id)
	if err != nil {
		return nil, fmt.Errorf("subagents: %w", err)
	}
	defer sr.Close()
	for sr.Next() {
		var ts string
		var s Subagent
		if err := sr.Scan(&ts, &s.Kind, &s.Task, &s.PromptNumber); err != nil {
			return nil, err
		}
		if s.At, err = dbtime.Parse(ts); err != nil {
			return nil, err
		}
		d.Subagents = append(d.Subagents, s)
	}
	return d, sr.Err()
}

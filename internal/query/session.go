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
	Prompts       []PromptRow // oldest first
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

	if d.Prompts, err = loadPrompts(ctx, db, `p.session_id = ?`, []any{id}, `p.seq`, 0, now); err != nil {
		return nil, err
	}
	number := map[int64]int64{}
	for i, p := range d.Prompts {
		number[p.ID] = int64(i + 1)
		d.Actions += p.Actions
		d.Flags += p.Flags
	}
	if n := len(d.Prompts); n > 0 {
		d.Running = d.Prompts[n-1].Running
		if d.Started.IsZero() {
			d.Started = d.Prompts[0].Started
		}
	}

	rows := map[string]*ActivityRow{}
	for _, r := range StatusRows {
		rows[r[0]+" "+r[1]] = &ActivityRow{Resource: r[0], Action: r[1]}
	}
	pr := rows["prompt request"]
	pr.Count = int64(len(d.Prompts))
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
SELECT a.ts, COALESCE(a.path, ''), COALESCE(a.summary, ''), COALESCE(a.prompt_id, 0)
FROM actions a WHERE a.session_id = ? AND a.resource = 'subagent' AND a.action = 'start' ORDER BY a.ts, a.id`, id)
	if err != nil {
		return nil, fmt.Errorf("subagents: %w", err)
	}
	defer sr.Close()
	for sr.Next() {
		var ts string
		var s Subagent
		var promptID int64
		if err := sr.Scan(&ts, &s.Kind, &s.Task, &promptID); err != nil {
			return nil, err
		}
		if s.At, err = dbtime.Parse(ts); err != nil {
			return nil, err
		}
		s.PromptNumber = number[promptID]
		d.Subagents = append(d.Subagents, s)
	}
	return d, sr.Err()
}

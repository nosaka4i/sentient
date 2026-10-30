package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nosaka4i/sentient/internal/background"
	"github.com/nosaka4i/sentient/internal/dbtime"
)

// minAgentLife matches the attribution's rule: an agent binary that ran only
// briefly (a helper, ripgrep) is not an agent session.
const minAgentLife = 10 * time.Second

// AgentRow is one agent process in the list.
type AgentRow struct {
	ID         string // the process's exec id
	Agent      string // claude
	Repo       string
	Version    string // the binary's name, e.g. 2.1.305
	Started    time.Time
	Stopped    time.Time // zero while it runs
	Running    bool
	Background int64 // background commands it ran
}

// ListAgents returns the agent processes sentient attributed, newest first.
// Runs of the agent's binary as a tool (ripgrep, see background.IsToolRun) and
// brief runs are not agents; they were attributed by older builds.
func ListAgents(ctx context.Context, db *sql.DB) ([]AgentRow, error) {
	rows, err := db.QueryContext(ctx, `
SELECT a.root_exec_id, COALESCE(s.agent, ''), COALESCE(s.repo_root, s.cwd, p.cwd, ''), p.binary,
       p.started_at, COALESCE(p.exited_at, ''),
       SUM(CASE WHEN COALESCE(a.background, '') <> '' THEN 1 ELSE 0 END)
FROM process_attribution a
JOIN processes p ON p.exec_id = a.root_exec_id
LEFT JOIN sessions s ON s.id = (SELECT r.session_id FROM process_attribution r WHERE r.exec_id = a.root_exec_id)
WHERE a.root_exec_id IS NOT NULL AND COALESCE(p.argv, '') NOT LIKE '["-G","--ignore-files"%' AND COALESCE(p.argv, '') NOT LIKE '["-S","dfs"%'
GROUP BY a.root_exec_id
ORDER BY p.started_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("agents: %w", err)
	}
	defer rows.Close()
	var out []AgentRow
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		if !a.Running && a.Stopped.Sub(a.Started) < minAgentLife {
			continue // a one-off run of the binary, not an agent session
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanAgent(sc interface{ Scan(...any) error }) (AgentRow, error) {
	var a AgentRow
	var dir, bin, started, stopped string
	if err := sc.Scan(&a.ID, &a.Agent, &dir, &bin, &started, &stopped, &a.Background); err != nil {
		return a, err
	}
	var err error
	if a.Started, err = dbtime.Parse(started); err != nil {
		return a, err
	}
	a.Running = stopped == ""
	if !a.Running {
		if a.Stopped, err = dbtime.Parse(stopped); err != nil {
			return a, err
		}
	}
	if dir != "" {
		a.Repo = filepath.Base(dir)
	}
	a.Version = filepath.Base(bin)
	return a, nil
}

// BackgroundRun is one run of a background command.
type BackgroundRun struct {
	At      time.Time
	Command string
	Key     string // the group it belongs to
}

// BackgroundGroup is every run of one background command.
type BackgroundGroup struct {
	Key         string
	Count       int64
	First, Last time.Time
	Latest      []BackgroundRun // the newest runs, newest first
}

// AgentDetail is one agent process and its background commands, per command.
type AgentDetail struct {
	AgentRow
	Dir      string
	Sessions []AgentSession
	Groups   []BackgroundGroup
}

// AgentSession is a session the agent worked in.
type AgentSession struct {
	ID string // the agent's own session id
}

// latestPerGroup is how many runs a group shows before the timeline takes over.
const latestPerGroup = 20

// GetAgent returns one agent process, or ErrNotFound.
func GetAgent(ctx context.Context, db *sql.DB, id string) (*AgentDetail, error) {
	row := db.QueryRowContext(ctx, `
SELECT a.root_exec_id, COALESCE(s.agent, ''), COALESCE(s.repo_root, s.cwd, p.cwd, ''), p.binary,
       p.started_at, COALESCE(p.exited_at, ''),
       SUM(CASE WHEN COALESCE(a.background, '') <> '' THEN 1 ELSE 0 END), COALESCE(p.cwd, '')
FROM process_attribution a
JOIN processes p ON p.exec_id = a.root_exec_id
LEFT JOIN sessions s ON s.id = (SELECT r.session_id FROM process_attribution r WHERE r.exec_id = a.root_exec_id)
WHERE a.root_exec_id = ?
GROUP BY a.root_exec_id`, id)
	d := &AgentDetail{}
	var dir, bin, started, stopped string
	err := row.Scan(&d.ID, &d.Agent, &dir, &bin, &started, &stopped, &d.Background, &d.Dir)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}
	if d.Started, err = dbtime.Parse(started); err != nil {
		return nil, err
	}
	if d.Running = stopped == ""; !d.Running {
		if d.Stopped, err = dbtime.Parse(stopped); err != nil {
			return nil, err
		}
	}
	if dir != "" {
		d.Repo = filepath.Base(dir)
	}
	d.Version = filepath.Base(bin)

	ss, err := db.QueryContext(ctx, `
SELECT s.agent_session_id FROM sessions s
WHERE s.id IN (SELECT DISTINCT session_id FROM process_attribution WHERE root_exec_id = ?)
ORDER BY s.started_at`, id)
	if err != nil {
		return nil, fmt.Errorf("agent sessions: %w", err)
	}
	for ss.Next() {
		var s AgentSession
		if err := ss.Scan(&s.ID); err != nil {
			ss.Close()
			return nil, err
		}
		d.Sessions = append(d.Sessions, s)
	}
	ss.Close()
	if err := ss.Err(); err != nil {
		return nil, err
	}

	gr, err := db.QueryContext(ctx, `
SELECT a.background, COUNT(*), MIN(p.started_at), MAX(p.started_at)
FROM process_attribution a JOIN processes p ON p.exec_id = a.exec_id
WHERE a.root_exec_id = ? AND COALESCE(a.background, '') <> ''
GROUP BY a.background ORDER BY COUNT(*) DESC, a.background`, id)
	if err != nil {
		return nil, fmt.Errorf("agent groups: %w", err)
	}
	for gr.Next() {
		var g BackgroundGroup
		var first, last string
		if err := gr.Scan(&g.Key, &g.Count, &first, &last); err != nil {
			gr.Close()
			return nil, err
		}
		if g.First, err = dbtime.Parse(first); err != nil {
			gr.Close()
			return nil, err
		}
		if g.Last, err = dbtime.Parse(last); err != nil {
			gr.Close()
			return nil, err
		}
		d.Groups = append(d.Groups, g)
	}
	gr.Close()
	if err := gr.Err(); err != nil {
		return nil, err
	}
	for i := range d.Groups {
		runs, err := backgroundRuns(ctx, db, id, d.Groups[i].Key, time.Time{}, time.Time{}, latestPerGroup)
		if err != nil {
			return nil, err
		}
		d.Groups[i].Latest = runs
	}
	return d, nil
}

// backgroundRuns lists an agent's background command runs, newest first, in
// [from, to] (a zero time is unbounded), of one group when key is not "".
func backgroundRuns(ctx context.Context, db *sql.DB, id, key string, from, to time.Time, limit int) ([]BackgroundRun, error) {
	q := `
SELECT p.started_at, p.binary, COALESCE(p.argv, ''), a.background
FROM process_attribution a JOIN processes p ON p.exec_id = a.exec_id
WHERE a.root_exec_id = ? AND COALESCE(a.background, '') <> ''`
	args := []any{id}
	if key != "" {
		q += ` AND a.background = ?`
		args = append(args, key)
	}
	if !from.IsZero() {
		q += ` AND p.started_at >= ?`
		args = append(args, dbtime.Format(from))
	}
	if !to.IsZero() {
		q += ` AND p.started_at <= ?`
		args = append(args, dbtime.Format(to))
	}
	q += ` ORDER BY p.started_at DESC, p.exec_id`
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("background runs: %w", err)
	}
	defer rows.Close()
	var out []BackgroundRun
	for rows.Next() {
		var at, bin, argv string
		var r BackgroundRun
		if err := rows.Scan(&at, &bin, &argv, &r.Key); err != nil {
			return nil, err
		}
		if r.At, err = dbtime.Parse(at); err != nil {
			return nil, err
		}
		r.Command = background.CommandLine(bin, argv)
		out = append(out, r)
	}
	return out, rows.Err()
}

// TimelineFilter narrows an agent's background commands.
type TimelineFilter struct {
	Text     string // case-insensitive, matched against the command line
	From, To time.Time
	Offset   int
	Limit    int // 0 = DefaultTimelinePage
}

// DefaultTimelinePage is how many runs a timeline page shows.
const DefaultTimelinePage = 100

// maxTimelineScan bounds how many runs a filtered timeline reads.
const maxTimelineScan = 50000

// Timeline is one page of an agent's background commands.
type Timeline struct {
	Runs  []BackgroundRun // newest first; oldest first when the filter has a time window
	Total int64           // runs matching the filter
}

// AgentTimeline lists an agent's background commands, newest first, that match
// the filter. It returns ErrNotFound for an unknown agent.
func AgentTimeline(ctx context.Context, db *sql.DB, id string, f TimelineFilter) (*Timeline, error) {
	var one int
	if err := db.QueryRowContext(ctx, `SELECT 1 FROM process_attribution WHERE root_exec_id = ? LIMIT 1`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}
	if f.Limit <= 0 {
		f.Limit = DefaultTimelinePage
	}
	runs, err := backgroundRuns(ctx, db, id, "", f.From, f.To, maxTimelineScan)
	if err != nil {
		return nil, err
	}
	if !f.From.IsZero() || !f.To.IsZero() {
		// a window reads in time order; with no window the newest come first
		slices.Reverse(runs)
	}
	text := strings.ToLower(strings.TrimSpace(f.Text))
	t := &Timeline{}
	for _, r := range runs {
		if text != "" && !strings.Contains(strings.ToLower(r.Command), text) {
			continue
		}
		if t.Total >= int64(f.Offset) && len(t.Runs) < f.Limit {
			t.Runs = append(t.Runs, r)
		}
		t.Total++
	}
	return t, nil
}

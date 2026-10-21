package query

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
)

// ActivityKinds are all 22 resource/action pairs, in the order the activity
// page shows them (grouped by resource). StatusRows is the dashboard's
// folded subset of these.
var ActivityKinds = [][2]string{
	{"file", "read"}, {"file", "create"}, {"file", "update"}, {"file", "delete"}, {"file", "rename"}, {"file", "chmod"},
	{"dir", "create"}, {"dir", "delete"},
	{"command", "execute"},
	{"process", "inspect"}, {"process", "kill"},
	{"network", "connect"},
	{"web", "search"}, {"web", "fetch"},
	{"git", "commit"}, {"git", "push"}, {"git", "force-push"}, {"git", "reset"},
	{"launch item", "add"},
	{"prompt", "request"},
	{"subagent", "start"},
	{"monitor", "gap"},
}

// DefaultActivityLimit is how many events the activity page lists.
const DefaultActivityLimit = 200

// ActivityRequest asks for the counts of a window and the events of one kind.
type ActivityRequest struct {
	Days       int    // the window is the last N days (>= 1)
	Resource   string // the kind whose events to list; "" = pick a default
	Action     string
	FailedOnly bool
	Limit      int    // 0 = DefaultActivityLimit
	Search     string // only events whose text contains this (case-insensitive)
}

// ActivityEvent is one thing the agent did, with the prompt and session it
// belongs to.
type ActivityEvent struct {
	At                     time.Time
	What                   string // the path, command, host or text
	Failed                 bool
	Added, Removed         int64
	PromptID               int64
	PromptText             string
	Agent, Repo, SessionID string
}

// Activity is what the activity page shows.
type Activity struct {
	HistoryBegins time.Time
	From, To      time.Time
	Rows          []ActivityRow // one per ActivityKinds, zeros included
	Resource      string        // the kind whose events are listed ("" = none)
	Action        string
	Events        []ActivityEvent // newest first
	EventsTotal   int64           // matching events, before the limit
}

// KnownKind reports whether resource/action is one of ActivityKinds.
func KnownKind(resource, action string) bool {
	for _, k := range ActivityKinds {
		if k[0] == resource && k[1] == action {
			return true
		}
	}
	return false
}

// GetActivity returns the counts of every kind in the window ending at now,
// and the events of the requested kind. A kind that is empty or unknown
// falls back to command/execute, or else the first kind that has events.
func GetActivity(ctx context.Context, db *sql.DB, req ActivityRequest, now time.Time) (*Activity, error) {
	days := max(req.Days, 1)
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultActivityLimit
	}
	a := &Activity{To: now, From: now.Add(-time.Duration(days) * 24 * time.Hour)}
	var err error
	if a.HistoryBegins, err = HistoryBegins(ctx, db); err != nil {
		return nil, err
	}
	f, t := dbtime.Format(a.From), dbtime.Format(a.To)

	rows := map[string]*ActivityRow{}
	for _, k := range ActivityKinds {
		rows[k[0]+" "+k[1]] = &ActivityRow{Resource: k[0], Action: k[1]}
	}
	if err := promptRow(ctx, db, f, t, rows["prompt request"]); err != nil {
		return nil, err
	}
	if err := kindTotals(ctx, db, f, t, rows); err != nil {
		return nil, err
	}
	if err := kindSummaries(ctx, db, f, t, rows); err != nil {
		return nil, err
	}
	for _, k := range ActivityKinds {
		a.Rows = append(a.Rows, *rows[k[0]+" "+k[1]])
	}

	a.Resource, a.Action = req.Resource, req.Action
	if !KnownKind(a.Resource, a.Action) {
		a.Resource, a.Action = "", ""
		if rows["command execute"].Count > 0 {
			a.Resource, a.Action = "command", "execute"
		} else {
			for _, r := range a.Rows {
				if r.Count > 0 {
					a.Resource, a.Action = r.Resource, r.Action
					break
				}
			}
		}
	}
	if a.Resource == "" {
		return a, nil
	}
	if a.Resource == "prompt" {
		err = promptEvents(ctx, db, f, t, strings.TrimSpace(req.Search), limit, a)
	} else {
		err = actionEvents(ctx, db, f, t, req.FailedOnly, strings.TrimSpace(req.Search), limit, a)
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

// kindTotals fills the count of every kind, plus failed commands and lines
// changed, which SQL can add up directly.
func kindTotals(ctx context.Context, db *sql.DB, f, t string, rows map[string]*ActivityRow) error {
	r, err := db.QueryContext(ctx, `
SELECT resource, action, COUNT(*), COALESCE(SUM(failed), 0), COALESCE(SUM(lines_added), 0), COALESCE(SUM(lines_removed), 0)
FROM actions WHERE ts >= ? AND ts < ? GROUP BY resource, action`, f, t)
	if err != nil {
		return fmt.Errorf("activity totals: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var res, act string
		var n, failed, added, removed int64
		if err := r.Scan(&res, &act, &n, &failed, &added, &removed); err != nil {
			return err
		}
		row, ok := rows[res+" "+act]
		if !ok {
			continue // a kind this page doesn't know
		}
		row.Count, row.Failed, row.Added, row.Removed = n, failed, added, removed
		switch {
		case failed > 0:
			row.Summary = fmt.Sprintf("%d failed", failed)
		case added > 0 || removed > 0:
			row.Summary = fmt.Sprintf("+%d −%d", added, removed)
		}
	}
	return r.Err()
}

// kindSummaries adds the parts that need a look at each row: file reads
// outside their repo, and the hosts of fetches and connections.
func kindSummaries(ctx context.Context, db *sql.DB, f, t string, rows map[string]*ActivityRow) error {
	r, err := db.QueryContext(ctx, `
SELECT a.resource, a.action, COALESCE(a.path, ''), COALESCE(s.repo_root, '')
FROM actions a LEFT JOIN sessions s ON s.id = a.session_id
WHERE a.ts >= ? AND a.ts < ? AND ((a.resource = 'file' AND a.action = 'read') OR (a.resource IN ('web', 'network') AND a.action IN ('fetch', 'connect')))`, f, t)
	if err != nil {
		return fmt.Errorf("activity summaries: %w", err)
	}
	defer r.Close()
	hosts := map[string]map[string]bool{}
	for r.Next() {
		var res, act, path, repo string
		if err := r.Scan(&res, &act, &path, &repo); err != nil {
			return err
		}
		key := res + " " + act
		if key == "file read" {
			if repo != "" && path != "" && !within(path, repo) {
				rows[key].Outside++
			}
			continue
		}
		if h := hostOf(path); h != "" {
			if hosts[key] == nil {
				hosts[key] = map[string]bool{}
			}
			hosts[key][h] = true
		}
	}
	if err := r.Err(); err != nil {
		return err
	}
	if o := rows["file read"].Outside; o > 0 {
		rows["file read"].Summary = fmt.Sprintf("%d outside repo", o)
	}
	for key, set := range hosts {
		rows[key].Hosts = joinLimited(keys(set), 3)
		rows[key].Summary = rows[key].Hosts
	}
	return nil
}

// hostOf is the host of a URL, or the host part of host:port.
func hostOf(s string) string {
	if u, err := url.Parse(s); err == nil && u.Hostname() != "" && strings.Contains(s, "://") {
		return u.Hostname()
	}
	return strings.TrimSpace(s)
}

// whatOf is the one-line description of an action.
func whatOf(resource, path, summary string) string {
	switch {
	case resource == "subagent" && path != "" && summary != "":
		return path + ": " + summary
	case path != "":
		return path
	}
	return summary
}

// likePattern turns a search into a LIKE pattern that matches it anywhere
// (use with ESCAPE '\'), so % and _ in the search match themselves.
func likePattern(s string) string {
	s = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
	return "%" + s + "%"
}

func actionEvents(ctx context.Context, db *sql.DB, f, t string, failedOnly bool, search string, limit int, a *Activity) error {
	where := `a.resource = ? AND a.action = ? AND a.ts >= ? AND a.ts < ?`
	args := []any{a.Resource, a.Action, f, t}
	if failedOnly {
		where += ` AND a.failed = 1`
	}
	if search != "" {
		where += ` AND (a.path LIKE ? ESCAPE '\' OR a.summary LIKE ? ESCAPE '\')`
		args = append(args, likePattern(search), likePattern(search))
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM actions a WHERE `+where, args...).Scan(&a.EventsTotal); err != nil {
		return fmt.Errorf("activity event count: %w", err)
	}
	r, err := db.QueryContext(ctx, `
SELECT a.ts, COALESCE(a.path, ''), COALESCE(a.summary, ''), a.failed, a.lines_added, a.lines_removed,
       COALESCE(a.prompt_id, 0), COALESCE(p.text, ''), COALESCE(s.agent, ''), COALESCE(s.repo_root, s.cwd, ''), COALESCE(s.agent_session_id, '')
FROM actions a LEFT JOIN prompts p ON p.id = a.prompt_id LEFT JOIN sessions s ON s.id = a.session_id
WHERE `+where+` ORDER BY a.ts DESC, a.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return fmt.Errorf("activity events: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var e ActivityEvent
		var ts, path, summary, dir string
		var failed int64
		if err := r.Scan(&ts, &path, &summary, &failed, &e.Added, &e.Removed, &e.PromptID, &e.PromptText, &e.Agent, &dir, &e.SessionID); err != nil {
			return err
		}
		if e.At, err = dbtime.Parse(ts); err != nil {
			return err
		}
		e.What, e.Failed = whatOf(a.Resource, path, summary), failed != 0
		if dir != "" {
			e.Repo = filepath.Base(dir)
		}
		a.Events = append(a.Events, e)
	}
	return r.Err()
}

// promptEvents lists the prompts of the window: "prompt request" events are
// the prompts themselves.
func promptEvents(ctx context.Context, db *sql.DB, f, t, search string, limit int, a *Activity) error {
	where, args := `p.started_at >= ? AND p.started_at < ?`, []any{f, t}
	if search != "" {
		where += ` AND p.text LIKE ? ESCAPE '\'`
		args = append(args, likePattern(search))
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM prompts p WHERE `+where, args...).Scan(&a.EventsTotal); err != nil {
		return fmt.Errorf("activity prompt count: %w", err)
	}
	r, err := db.QueryContext(ctx, `
SELECT p.started_at, p.id, p.text, s.agent, COALESCE(s.repo_root, s.cwd, ''), s.agent_session_id
FROM prompts p JOIN sessions s ON s.id = p.session_id
WHERE `+where+` ORDER BY p.started_at DESC, p.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return fmt.Errorf("activity prompts: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var e ActivityEvent
		var ts, dir string
		if err := r.Scan(&ts, &e.PromptID, &e.PromptText, &e.Agent, &dir, &e.SessionID); err != nil {
			return err
		}
		if e.At, err = dbtime.Parse(ts); err != nil {
			return err
		}
		e.What = e.PromptText
		if dir != "" {
			e.Repo = filepath.Base(dir)
		}
		a.Events = append(a.Events, e)
	}
	return r.Err()
}

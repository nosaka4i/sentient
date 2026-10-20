// Package query holds the read queries shared by the CLI views and (later)
// the web API, so they never disagree. It runs inside sentientd, the only
// process that opens the store.
package query

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
)

// ActivityRow is one RESOURCE/ACTION row of the activity section
// (LAST 24H in sen status).
type ActivityRow struct {
	Resource string
	Action   string
	Count    int64
	Summary  string // the CLI's one-line SUMMARY, built from the fields below

	// Structured summary parts, for views that style them (the web tiles).
	Repos   int64  // prompt request: distinct repos
	Outside int64  // file read: outside the session's repo
	Added   int64  // file update: lines added
	Removed int64  // file update: lines removed
	Failed  int64  // command execute: failed
	Hosts   string // web: fetched hosts, first few then "+N more"
}

// Status is the data behind `sen status`.
type Status struct {
	// HistoryBegins is the earliest ingested record (zero if none).
	HistoryBegins time.Time
	// Activity holds exactly the rows of StatusRows, in that order, zero
	// counts included so the layout never shifts.
	Activity []ActivityRow
	// UnknownRecords counts stored log records of unknown type or
	// unparsable lines (shown as a warning; they are never skipped).
	UnknownRecords int64
	// PromptsTotal is every prompt started in the window; Prompts holds the
	// newest few of them.
	PromptsTotal int64
	Prompts      []PromptRow
}

// DefaultPromptLimit is how many prompts sen status and the dashboard show.
const DefaultPromptLimit = 5

// StatusRows are the rows of the activity summary, in display order
// (docs/mocks/cli/status.md). All resource/action counts are a step
// deeper, in `sen activity`.
var StatusRows = [][2]string{
	{"prompt", "request"},
	{"file", "read"},
	{"file", "create"},
	{"file", "update"},
	{"file", "delete"},
	{"command", "execute"},
	{"process", "inspect"},
	{"network", "connect"},
	{"git", "commit"},
	{"web", "search"},
	{"subagent", "start"},
}

// folds maps minor actions onto the summary row they count under. Risky
// actions (force-push, reset, chmod, process kill, launch item add,
// monitor gap) are not folded anywhere: they raise alerts instead.
var folds = map[string]string{
	"web fetch":   "web search",
	"git push":    "git commit",
	"file rename": "file update",
	"dir create":  "file create",
	"dir delete":  "file delete",
}

// summaryRow returns the summary row an action counts under, or "".
func summaryRow(res, act string) string {
	key := res + " " + act
	if f, ok := folds[key]; ok {
		return f
	}
	for _, r := range StatusRows {
		if r[0] == res && r[1] == act {
			return key
		}
	}
	return ""
}

// GetStatus returns status for the window from <= ts < to, with the newest
// promptLimit prompts (DefaultPromptLimit if <= 0).
func GetStatus(ctx context.Context, db *sql.DB, from, to time.Time, promptLimit int) (*Status, error) {
	st := &Status{}
	var err error
	if st.HistoryBegins, err = HistoryBegins(ctx, db); err != nil {
		return nil, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM raw_log_records WHERE unknown = 1`).Scan(&st.UnknownRecords); err != nil {
		return nil, fmt.Errorf("unknown records: %w", err)
	}

	f, t := dbtime.Format(from), dbtime.Format(to)
	rows := map[string]*ActivityRow{}
	for _, r := range StatusRows {
		rows[r[0]+" "+r[1]] = &ActivityRow{Resource: r[0], Action: r[1]}
	}
	if err := promptRow(ctx, db, f, t, rows["prompt request"]); err != nil {
		return nil, err
	}
	if err := actionRows(ctx, db, f, t, rows); err != nil {
		return nil, err
	}
	for _, r := range StatusRows {
		st.Activity = append(st.Activity, *rows[r[0]+" "+r[1]])
	}
	if promptLimit <= 0 {
		promptLimit = DefaultPromptLimit
	}
	if st.PromptsTotal, st.Prompts, err = RecentPrompts(ctx, db, from, to, promptLimit); err != nil {
		return nil, err
	}
	return st, nil
}

func promptRow(ctx context.Context, db *sql.DB, from, to string, row *ActivityRow) error {
	r, err := db.QueryContext(ctx, `
SELECT COALESCE(s.repo_root, s.cwd, '') FROM prompts p JOIN sessions s ON s.id = p.session_id
WHERE p.started_at >= ? AND p.started_at < ?`, from, to)
	if err != nil {
		return fmt.Errorf("prompts: %w", err)
	}
	defer r.Close()
	repos := map[string]bool{}
	for r.Next() {
		var repo string
		if err := r.Scan(&repo); err != nil {
			return err
		}
		row.Count++
		if repo != "" {
			repos[repo] = true
		}
	}
	if err := r.Err(); err != nil {
		return err
	}
	row.Repos = int64(len(repos))
	if len(repos) > 0 {
		row.Summary = plural(len(repos), "repo")
	}
	return nil
}

func actionRows(ctx context.Context, db *sql.DB, from, to string, rows map[string]*ActivityRow) error {
	r, err := db.QueryContext(ctx, `
SELECT a.resource, a.action, COALESCE(a.path, ''), a.failed, a.lines_added, a.lines_removed, COALESCE(s.repo_root, '')
FROM actions a LEFT JOIN sessions s ON s.id = a.session_id
WHERE a.ts >= ? AND a.ts < ?`, from, to)
	if err != nil {
		return fmt.Errorf("actions: %w", err)
	}
	defer r.Close()

	var failed, outside, added, removed int64
	hosts := map[string]bool{}
	for r.Next() {
		var res, act, path, repo string
		var f, add, rm int64
		if err := r.Scan(&res, &act, &path, &f, &add, &rm, &repo); err != nil {
			return err
		}
		key := summaryRow(res, act)
		if key == "" {
			continue // not a summary row (risky actions surface as alerts)
		}
		rows[key].Count++
		switch key {
		case "file read":
			if repo != "" && path != "" && !within(path, repo) {
				outside++
			}
		case "file update":
			added += add
			removed += rm
		case "command execute":
			failed += f
		case "web search":
			if act == "fetch" {
				if u, err := url.Parse(path); err == nil && u.Hostname() != "" {
					hosts[u.Hostname()] = true
				}
			}
		}
	}
	if err := r.Err(); err != nil {
		return err
	}
	rows["file read"].Outside = outside
	if outside > 0 {
		rows["file read"].Summary = fmt.Sprintf("%d outside repo", outside)
	}
	rows["file update"].Added, rows["file update"].Removed = added, removed
	if added > 0 || removed > 0 {
		rows["file update"].Summary = fmt.Sprintf("+%d −%d", added, removed)
	}
	rows["command execute"].Failed = failed
	if failed > 0 {
		rows["command execute"].Summary = fmt.Sprintf("%d failed", failed)
	}
	if len(hosts) > 0 {
		rows["web search"].Hosts = joinLimited(keys(hosts), 3)
		rows["web search"].Summary = rows["web search"].Hosts
	}
	return nil
}

// PromptActionCounts returns, for each prompt started in [from, to), the
// number of actions on the system linked to it (see systemAction):
// commands (one per simple command) plus file, network, git, web fetch…
// actions. This is the ACTIONS column of PROMPTS.
// Prompts with no actions are present with 0.
func PromptActionCounts(ctx context.Context, db *sql.DB, from, to time.Time) (map[int64]int64, error) {
	r, err := db.QueryContext(ctx, `
SELECT p.id, COUNT(a.id) FROM prompts p LEFT JOIN actions a ON a.prompt_id = p.id AND `+systemAction+`
WHERE p.started_at >= ? AND p.started_at < ?
GROUP BY p.id`, dbtime.Format(from), dbtime.Format(to))
	if err != nil {
		return nil, fmt.Errorf("prompt actions: %w", err)
	}
	defer r.Close()
	out := map[int64]int64{}
	for r.Next() {
		var id, n int64
		if err := r.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, r.Err()
}

// within reports whether path is inside dir.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// joinLimited joins the first n items and says how many more there are.
func joinLimited(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s +%d more", strings.Join(items[:n], ", "), len(items)-n)
}

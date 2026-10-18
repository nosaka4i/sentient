package query

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nosaka4i/sentient/internal/dbtime"
)

// PromptRow is one row of PROMPTS (sen status, dashboard).
type PromptRow struct {
	ID        int64
	Text      string // full prompt text
	Started   time.Time
	Duration  time.Duration // start to the session's last activity within the prompt
	Running   bool
	Agent     string
	Repo      string // repo (or cwd) basename
	SessionID string // the agent's session id
	Actions   int64  // all actions linked to the prompt
	Flags     int64  // open alerts the prompt caused
}

// RunningWindow: a session's latest prompt counts as still running when
// the session had activity this recently. The log has no reliable
// "turn finished" marker across versions; kernel process state will
// replace this guess.
const RunningWindow = 5 * time.Minute

// RecentPrompts returns the total number of prompts started in [from, to)
// and the newest `limit` of them, newest first.
func RecentPrompts(ctx context.Context, db *sql.DB, from, to time.Time, limit int) (int64, []PromptRow, error) {
	f, t := dbtime.Format(from), dbtime.Format(to)
	var total int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM prompts WHERE started_at >= ? AND started_at < ?`, f, t).Scan(&total); err != nil {
		return 0, nil, fmt.Errorf("prompt total: %w", err)
	}
	r, err := db.QueryContext(ctx, `
SELECT p.id, p.session_id, p.text, p.started_at, p.ended_at, s.agent, COALESCE(s.repo_root, s.cwd, ''), s.agent_session_id
FROM prompts p JOIN sessions s ON s.id = p.session_id
WHERE p.started_at >= ? AND p.started_at < ?
ORDER BY p.started_at DESC, p.id DESC LIMIT ?`, f, t, limit)
	if err != nil {
		return 0, nil, fmt.Errorf("prompts: %w", err)
	}
	type raw struct {
		row     PromptRow
		session int64
		start   string
		end     sql.NullString
	}
	var raws []raw
	for r.Next() {
		var x raw
		var dir string
		if err := r.Scan(&x.row.ID, &x.session, &x.row.Text, &x.start, &x.end, &x.row.Agent, &dir, &x.row.SessionID); err != nil {
			r.Close()
			return 0, nil, err
		}
		if dir != "" {
			x.row.Repo = filepath.Base(dir)
		}
		raws = append(raws, x)
	}
	r.Close()
	if err := r.Err(); err != nil {
		return 0, nil, err
	}

	rows := make([]PromptRow, 0, len(raws))
	for _, x := range raws {
		p := x.row
		if p.Started, err = dbtime.Parse(x.start); err != nil {
			return 0, nil, err
		}
		end := t
		if x.end.Valid {
			end = x.end.String
		}
		var last sql.NullString
		if err := db.QueryRowContext(ctx, `
SELECT MAX(ts) FROM raw_log_records WHERE session_id = ? AND ts >= ? AND ts < ?`, x.session, x.start, end).Scan(&last); err != nil {
			return 0, nil, fmt.Errorf("prompt activity: %w", err)
		}
		if last.Valid {
			lt, err := dbtime.Parse(last.String)
			if err != nil {
				return 0, nil, err
			}
			p.Duration = lt.Sub(p.Started)
			p.Running = !x.end.Valid && to.Sub(lt) <= RunningWindow
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM actions WHERE prompt_id = ?`, p.ID).Scan(&p.Actions); err != nil {
			return 0, nil, fmt.Errorf("prompt actions: %w", err)
		}
		if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM alerts al JOIN actions a ON a.id = al.action_id
WHERE a.prompt_id = ? AND al.status = 'open'`, p.ID).Scan(&p.Flags); err != nil {
			return 0, nil, fmt.Errorf("prompt flags: %w", err)
		}
		rows = append(rows, p)
	}
	return total, rows, nil
}

var pastedRE = regexp.MustCompile(`(?s)<pasted_content[^>]*>(.*?)</pasted_content>`)

// PromptLine reduces a prompt to what a table row shows: the number of
// pasted lines (Claude Code wraps pastes in <pasted_content>), and the
// first non-empty line of the rest with whitespace collapsed.
func PromptLine(text string) (pastedLines int, line string) {
	rest := pastedRE.ReplaceAllStringFunc(text, func(m string) string {
		body := strings.Trim(pastedRE.FindStringSubmatch(m)[1], "\n")
		if body != "" {
			pastedLines += strings.Count(body, "\n") + 1
		}
		return "\n"
	})
	for _, l := range strings.Split(rest, "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			return pastedLines, strings.Join(f, " ")
		}
	}
	return pastedLines, ""
}

// Ellipsis shortens s to at most n runes, ending in "…" when cut.
func Ellipsis(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:n-1]), " ") + "…"
}

// ShortDuration formats a prompt's duration: `<1m`, `12m`, `1h 5m`.
func ShortDuration(d time.Duration) string {
	m := int(d / time.Minute)
	switch {
	case m < 1:
		return "<1m"
	case m < 60:
		return fmt.Sprintf("%dm", m)
	case m%60 == 0:
		return fmt.Sprintf("%dh", m/60)
	default:
		return fmt.Sprintf("%dh %dm", m/60, m%60)
	}
}

// ShortSession is the session id as shown in tables (first 4 characters).
func ShortSession(id string) string {
	if len(id) > 4 {
		return id[:4]
	}
	return id
}

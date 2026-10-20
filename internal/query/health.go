package query

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/health"
)

// Adapters are the agent log adapters sentientd runs, with where each
// reads from. Shown on /health even before an agent has written anything.
var Adapters = []health.Agent{
	{Agent: "claude", Logs: "~/.claude/projects"},
}

// HistoryBegins returns the oldest ingested record's time (zero if none).
func HistoryBegins(ctx context.Context, db *sql.DB) (time.Time, error) {
	var begins sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT MIN(ts) FROM raw_log_records WHERE ts IS NOT NULL`).Scan(&begins); err != nil {
		return time.Time{}, fmt.Errorf("history begins: %w", err)
	}
	if !begins.Valid {
		return time.Time{}, nil
	}
	return dbtime.Parse(begins.String)
}

// IngestStats returns each adapter's progress: its newest record, files
// tracked and unknown records. Records that can't be tied to a session
// (unparsable lines) count under the only adapter there is so far.
func IngestStats(ctx context.Context, db *sql.DB) ([]health.IngestAgent, error) {
	byAgent := map[string]*health.IngestAgent{}
	for _, a := range Adapters {
		byAgent[a.Agent] = &health.IngestAgent{Agent: a.Agent}
	}
	get := func(agent string) *health.IngestAgent {
		if byAgent[agent] == nil {
			byAgent[agent] = &health.IngestAgent{Agent: agent}
		}
		return byAgent[agent]
	}

	rows, err := db.QueryContext(ctx, `SELECT agent, MAX(ended_at) FROM sessions GROUP BY agent`)
	if err != nil {
		return nil, fmt.Errorf("ingest last record: %w", err)
	}
	for rows.Next() {
		var agent string
		var last sql.NullString
		if err := rows.Scan(&agent, &last); err != nil {
			rows.Close()
			return nil, err
		}
		if last.Valid {
			t, err := dbtime.Parse(last.String)
			if err != nil {
				rows.Close()
				return nil, err
			}
			get(agent).LastRecord = t
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = db.QueryContext(ctx, `
SELECT COALESCE(s.agent, ?), COUNT(*) FROM raw_log_records r LEFT JOIN sessions s ON s.id = r.session_id
WHERE r.unknown = 1 GROUP BY 1`, Adapters[0].Agent)
	if err != nil {
		return nil, fmt.Errorf("ingest unknown: %w", err)
	}
	for rows.Next() {
		var agent string
		var n int64
		if err := rows.Scan(&agent, &n); err != nil {
			rows.Close()
			return nil, err
		}
		get(agent).Unknown = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Transcript files: all from the Claude Code adapter so far.
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ingest_files`).Scan(&get(Adapters[0].Agent).Files); err != nil {
		return nil, fmt.Errorf("ingest files: %w", err)
	}

	out := make([]health.IngestAgent, 0, len(byAgent))
	for _, a := range byAgent {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	return out, nil
}

// Agents returns the agents sentient reads, each with the newest version
// seen in its records.
func Agents(ctx context.Context, db *sql.DB) ([]health.Agent, error) {
	out := append([]health.Agent(nil), Adapters...)
	for i := range out {
		var v sql.NullString
		err := db.QueryRowContext(ctx, `
SELECT agent_version FROM sessions WHERE agent = ? AND agent_version IS NOT NULL
ORDER BY ended_at DESC LIMIT 1`, out[i].Agent).Scan(&v)
		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("agent version: %w", err)
		}
		out[i].Version = v.String
	}
	return out, nil
}

// LiveSessions returns sessions with log activity since `since`, most
// recent first.
func LiveSessions(ctx context.Context, db *sql.DB, since time.Time) ([]health.LiveSession, error) {
	rows, err := db.QueryContext(ctx, `
SELECT agent_session_id, agent, COALESCE(repo_root, cwd, ''), ended_at FROM sessions
WHERE ended_at >= ? ORDER BY ended_at DESC`, dbtime.Format(since))
	if err != nil {
		return nil, fmt.Errorf("live sessions: %w", err)
	}
	defer rows.Close()
	var out []health.LiveSession
	for rows.Next() {
		var l health.LiveSession
		var dir, last string
		if err := rows.Scan(&l.SessionID, &l.Agent, &dir, &last); err != nil {
			return nil, err
		}
		if dir != "" {
			l.Repo = filepath.Base(dir)
		}
		if l.LastActivity, err = dbtime.Parse(last); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

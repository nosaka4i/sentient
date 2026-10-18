package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Tx is a write transaction. The ingester does one per batch of records.
type Tx struct {
	tx *sql.Tx
}

// Begin starts a write transaction.
func (s *Store) Begin(ctx context.Context) (*Tx, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &Tx{tx: tx}, nil
}

// Commit commits the transaction.
func (t *Tx) Commit() error { return t.tx.Commit() }

// Rollback abandons the transaction (no-op after Commit).
func (t *Tx) Rollback() error { return t.tx.Rollback() }

// Session describes a session as seen in a log record.
type Session struct {
	Agent          string
	AgentSessionID string
	Cwd            string
	RepoRoot       string
	TS             string // record timestamp, may be empty
}

// UpsertSession returns the session's id, creating it on first sight and
// widening its started_at/ended_at to cover ts. cwd and repo_root are set
// from the first record that has them.
func (t *Tx) UpsertSession(ctx context.Context, s Session) (int64, error) {
	var ts any
	if s.TS != "" {
		ts = s.TS
	}
	_, err := t.tx.ExecContext(ctx, `
INSERT INTO sessions (agent, agent_session_id, repo_root, cwd, started_at, ended_at)
VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?)
ON CONFLICT (agent, agent_session_id) DO UPDATE SET
	cwd        = COALESCE(sessions.cwd, excluded.cwd),
	repo_root  = COALESCE(sessions.repo_root, excluded.repo_root),
	started_at = CASE WHEN sessions.started_at IS NULL OR excluded.started_at < sessions.started_at
	                  THEN COALESCE(excluded.started_at, sessions.started_at) ELSE sessions.started_at END,
	ended_at   = CASE WHEN sessions.ended_at IS NULL OR excluded.ended_at > sessions.ended_at
	                  THEN COALESCE(excluded.ended_at, sessions.ended_at) ELSE sessions.ended_at END`,
		s.Agent, s.AgentSessionID, s.RepoRoot, s.Cwd, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("upsert session: %w", err)
	}
	var id int64
	err = t.tx.QueryRowContext(ctx, `SELECT id FROM sessions WHERE agent = ? AND agent_session_id = ?`,
		s.Agent, s.AgentSessionID).Scan(&id)
	return id, err
}

// SessionRepoRoot returns the session's repo root and cwd ("" if unknown).
func (t *Tx) SessionRepoRoot(ctx context.Context, sessionID int64) (repoRoot, cwd string, err error) {
	var rr, c sql.NullString
	err = t.tx.QueryRowContext(ctx, `SELECT repo_root, cwd FROM sessions WHERE id = ?`, sessionID).Scan(&rr, &c)
	return rr.String, c.String, err
}

// RawRecord is one agent log record, stored verbatim.
type RawRecord struct {
	SessionID  int64 // 0 = not attributable to a session
	TS         string
	SourceFile string
	Type       string
	Unknown    bool
	Record     string
}

// AppendRaw appends a record to the hash chain and returns its id.
func (t *Tx) AppendRaw(ctx context.Context, r RawRecord) (int64, error) {
	var prev string
	err := t.tx.QueryRowContext(ctx, `SELECT hash FROM raw_log_records ORDER BY id DESC LIMIT 1`).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("read chain head: %w", err)
	}
	hash := ChainHash(prev, r.TS, r.SourceFile, r.Record)
	res, err := t.tx.ExecContext(ctx, `
INSERT INTO raw_log_records (session_id, ts, source_file, type, unknown, record, prev_hash, hash)
VALUES (NULLIF(?, 0), NULLIF(?, ''), ?, ?, ?, ?, ?, ?)`,
		r.SessionID, r.TS, r.SourceFile, r.Type, boolInt(r.Unknown), r.Record, prev, hash)
	if err != nil {
		return 0, fmt.Errorf("append raw record: %w", err)
	}
	return res.LastInsertId()
}

// AddPrompt records a user prompt as the next in its session.
func (t *Tx) AddPrompt(ctx context.Context, sessionID int64, text, ts string) (int64, error) {
	// The previous prompt ends when the next one starts.
	if _, err := t.tx.ExecContext(ctx, `
UPDATE prompts SET ended_at = ? WHERE session_id = ? AND seq = (SELECT MAX(seq) FROM prompts WHERE session_id = ?)`,
		ts, sessionID, sessionID); err != nil {
		return 0, err
	}
	res, err := t.tx.ExecContext(ctx, `
INSERT INTO prompts (session_id, seq, text, started_at)
VALUES (?, COALESCE((SELECT MAX(seq) FROM prompts WHERE session_id = ?), 0) + 1, ?, ?)`,
		sessionID, sessionID, text, ts)
	if err != nil {
		return 0, fmt.Errorf("add prompt: %w", err)
	}
	return res.LastInsertId()
}

// PromptAt returns the prompt running in the session at ts (0 if none).
func (t *Tx) PromptAt(ctx context.Context, sessionID int64, ts string) (int64, error) {
	var id int64
	err := t.tx.QueryRowContext(ctx, `
SELECT id FROM prompts WHERE session_id = ? AND started_at <= ? ORDER BY started_at DESC, seq DESC LIMIT 1`,
		sessionID, ts).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// Action is a normalized action.
type Action struct {
	SessionID   int64
	PromptID    int64
	TS          string
	Resource    string
	Action      string
	Path        string
	Summary     string
	Evidence    string // kernel | log | both
	RawRecordID int64
	ToolUseID   string
	Part        int // index within the tool call (Bash: one per simple command)
}

// AddAction inserts an action. An action whose (tool_use_id, part) already
// exists is ignored (Claude Code may write the same tool call twice).
func (t *Tx) AddAction(ctx context.Context, a Action) error {
	_, err := t.tx.ExecContext(ctx, `
INSERT INTO actions (session_id, prompt_id, ts, resource, action, path, summary, evidence, raw_record_id, tool_use_id, part)
VALUES (NULLIF(?, 0), NULLIF(?, 0), ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, 0), NULLIF(?, ''), ?)
ON CONFLICT DO NOTHING`,
		a.SessionID, a.PromptID, a.TS, a.Resource, a.Action, a.Path, a.Summary, a.Evidence, a.RawRecordID, a.ToolUseID, a.Part)
	if err != nil {
		return fmt.Errorf("add action: %w", err)
	}
	return nil
}

// ToolResult is what a tool call's result tells us about its action.
type ToolResult struct {
	ToolUseID    string
	Failed       bool
	Action       string // if set, replaces the action verb (e.g. Write: create vs update)
	LinesAdded   int
	LinesRemoved int
}

// ApplyToolResult updates the actions created from the matching tool call.
// A failure marks only the last part: a command line's exit status is its
// last command's, and which earlier part failed is unknown from the log
// (the kernel will tell). It reports whether such an action exists.
func (t *Tx) ApplyToolResult(ctx context.Context, r ToolResult) (bool, error) {
	res, err := t.tx.ExecContext(ctx, `
UPDATE actions SET
	failed        = CASE WHEN part = (SELECT MAX(part) FROM actions a2 WHERE a2.tool_use_id = actions.tool_use_id)
	                     THEN ? ELSE 0 END,
	action        = COALESCE(NULLIF(?, ''), action),
	lines_added   = ?,
	lines_removed = ?
WHERE tool_use_id = ?`,
		boolInt(r.Failed), r.Action, r.LinesAdded, r.LinesRemoved, r.ToolUseID)
	if err != nil {
		return false, fmt.Errorf("apply tool result: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Offset returns how far the file has been ingested.
func (s *Store) Offset(ctx context.Context, path string) (int64, error) {
	var off int64
	err := s.DB.QueryRowContext(ctx, `SELECT offset FROM ingest_files WHERE path = ?`, path).Scan(&off)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return off, err
}

// SetOffset records ingest progress, in the same transaction as the records.
func (t *Tx) SetOffset(ctx context.Context, path string, off int64) error {
	_, err := t.tx.ExecContext(ctx, `
INSERT INTO ingest_files (path, offset) VALUES (?, ?)
ON CONFLICT (path) DO UPDATE SET offset = excluded.offset`, path, off)
	return err
}

// VerifyChain walks raw_log_records in order and returns the id of the
// first record whose hash doesn't match (0 if the chain is intact).
func (s *Store) VerifyChain(ctx context.Context) (int64, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, COALESCE(ts, ''), source_file, record, prev_hash, hash FROM raw_log_records ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	prev := ""
	for rows.Next() {
		var id int64
		var ts, src, rec, ph, h string
		if err := rows.Scan(&id, &ts, &src, &rec, &ph, &h); err != nil {
			return 0, err
		}
		if ph != prev || ChainHash(prev, ts, src, rec) != h {
			return id, nil
		}
		prev = h
	}
	return 0, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

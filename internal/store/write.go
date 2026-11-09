package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
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
	AgentVersion   string // agent version in the record, may be empty
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
INSERT INTO sessions (agent, agent_session_id, repo_root, cwd, started_at, ended_at, agent_version)
VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, NULLIF(?, ''))
ON CONFLICT (agent, agent_session_id) DO UPDATE SET
	agent_version = COALESCE(excluded.agent_version, sessions.agent_version),
	cwd        = COALESCE(sessions.cwd, excluded.cwd),
	repo_root  = COALESCE(sessions.repo_root, excluded.repo_root),
	started_at = CASE WHEN sessions.started_at IS NULL OR excluded.started_at < sessions.started_at
	                  THEN COALESCE(excluded.started_at, sessions.started_at) ELSE sessions.started_at END,
	ended_at   = CASE WHEN sessions.ended_at IS NULL OR excluded.ended_at > sessions.ended_at
	                  THEN COALESCE(excluded.ended_at, sessions.ended_at) ELSE sessions.ended_at END`,
		s.Agent, s.AgentSessionID, s.RepoRoot, s.Cwd, ts, ts, s.AgentVersion)
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
		r.SessionID, r.TS, r.SourceFile, r.Type, boolInt(r.Unknown), encodeRecord(r.Record), prev, hash)
	if err != nil {
		return 0, fmt.Errorf("append raw record: %w", err)
	}
	// The record is activity of the session's latest prompt that had started.
	if r.SessionID != 0 && r.TS != "" {
		if _, err := t.tx.ExecContext(ctx, `
UPDATE prompts SET last_activity_at = ?
WHERE session_id = ? AND seq = (SELECT MAX(seq) FROM prompts WHERE session_id = ? AND started_at <= ?)
  AND (last_activity_at IS NULL OR last_activity_at < ?)`,
			r.TS, r.SessionID, r.SessionID, r.TS, r.TS); err != nil {
			return 0, fmt.Errorf("record prompt activity: %w", err)
		}
	}
	return res.LastInsertId()
}

// AddPrompt records a user prompt as the next in its session.
func (t *Tx) AddPrompt(ctx context.Context, sessionID int64, text, ts string) (int64, error) {
	// The previous prompt ends when the next one starts. Its last activity is
	// the last record before that: records are appended before the prompt
	// they open exists, so AppendRaw may have credited this one too early.
	if _, err := t.tx.ExecContext(ctx, `
UPDATE prompts SET ended_at = ?,
	last_activity_at = COALESCE((SELECT MAX(r.ts) FROM raw_log_records r
		WHERE r.session_id = prompts.session_id AND r.ts >= prompts.started_at AND r.ts < ?), prompts.started_at)
WHERE session_id = ? AND seq = (SELECT MAX(seq) FROM prompts WHERE session_id = ?)`,
		ts, ts, sessionID, sessionID); err != nil {
		return 0, err
	}
	res, err := t.tx.ExecContext(ctx, `
INSERT INTO prompts (session_id, seq, text, started_at, last_activity_at)
VALUES (?, COALESCE((SELECT MAX(seq) FROM prompts WHERE session_id = ?), 0) + 1, ?, ?, ?)`,
		sessionID, sessionID, text, ts, ts)
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
	// CallCommand is the whole Bash command line, set on the first part only.
	CallCommand string
	// ExecID is the kernel exec behind a kernel-only action.
	ExecID string
}

// AddAction inserts an action. An action whose (tool_use_id, part) already
// exists is ignored (Claude Code may write the same tool call twice).
func (t *Tx) AddAction(ctx context.Context, a Action) error {
	res, err := t.tx.ExecContext(ctx, `
INSERT INTO actions (session_id, prompt_id, ts, resource, action, path, summary, evidence, raw_record_id, tool_use_id, part, call_command)
VALUES (NULLIF(?, 0), NULLIF(?, 0), ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, 0), NULLIF(?, ''), ?, NULLIF(?, ''))
ON CONFLICT DO NOTHING`,
		a.SessionID, a.PromptID, a.TS, a.Resource, a.Action, a.Path, a.Summary, a.Evidence, a.RawRecordID, a.ToolUseID, a.Part, a.CallCommand)
	if err != nil {
		return fmt.Errorf("add action: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 && a.PromptID != 0 && IsSystemAction(a.Resource, a.Action) {
		if _, err := t.tx.ExecContext(ctx, `UPDATE prompts SET action_count = action_count + 1 WHERE id = ?`, a.PromptID); err != nil {
			return fmt.Errorf("count action: %w", err)
		}
	}
	return nil
}

// IsSystemAction reports whether an action acts on this machine, which is
// what a prompt's ACTIONS count (docs/design.md "Counting commands"): file,
// dir, command, process, network, git, web fetch and launch item. The
// prompt itself, subagent starts and server-side web searches are not.
// Keep in step with query's systemAction SQL.
func IsSystemAction(resource, action string) bool {
	switch resource {
	case "file", "dir", "command", "process", "network", "git", "launch item":
		return true
	case "web":
		return action == "fetch"
	}
	return false
}

// ToolResult is what a tool call's result tells us about its action.
type ToolResult struct {
	ToolUseID    string
	TS           string // when the result was logged
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
	lines_removed = ?,
	result_ts     = NULLIF(?, '')
WHERE tool_use_id = ?`,
		boolInt(r.Failed), r.Action, r.LinesAdded, r.LinesRemoved, r.TS, r.ToolUseID)
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
	r, err := s.VerifyChainFrom(ctx, ChainPoint{})
	return r.BadID, err
}

// ChainPoint is a verified position in the chain: the last record checked
// and its hash. The zero value is the start of the chain.
type ChainPoint struct {
	ID   int64
	Hash string
}

// ChainResult is the outcome of a chain check.
type ChainResult struct {
	Last    ChainPoint // where the check ended (resume from here next time)
	Checked int64      // records checked
	BadID   int64      // first mismatching record (0 = intact)
}

// VerifyChainFrom checks the records after `from`, given that `from` was
// already verified. Starting from the zero point checks everything;
// resuming from the last result checks only what was appended since,
// which is cheap.
//
// It reads in slices and gives the database connection back between them: the
// store has one connection, so a single query held open over every record would
// make every other request (the Health page, the ingest, the API) wait for the
// whole check. VerifyChainPaced also pauses between slices.
func (s *Store) VerifyChainFrom(ctx context.Context, from ChainPoint) (ChainResult, error) {
	return s.VerifyChainPaced(ctx, from, VerifyPace{})
}

// VerifyPace is how hard a chain check works: it reads Slice records, lets go of
// the database connection, waits Pause, and carries on. A longer pause or a
// smaller slice takes longer and gets in the way less.
type VerifyPace struct {
	Slice int           // records per slice (default DefaultVerifySlice)
	Pause time.Duration // wait between slices (default none)
	// Between, if set, is called after each full slice, with the connection free
	// (tests use it).
	Between func()
}

// DefaultVerifySlice is the records one slice reads: about 80 ms of work on the
// first database this was timed on.
const DefaultVerifySlice = 2000

func (p VerifyPace) slice() int {
	if p.Slice <= 0 {
		return DefaultVerifySlice
	}
	return p.Slice
}

// wait pauses between slices; false means ctx ended during the wait.
func (p VerifyPace) wait(ctx context.Context) bool {
	if p.Between != nil {
		p.Between()
	}
	if p.Pause <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(p.Pause)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// VerifyChainPaced is VerifyChainFrom at the given pace. If ctx ends mid-check it
// returns the context's error.
func (s *Store) VerifyChainPaced(ctx context.Context, from ChainPoint, pace VerifyPace) (ChainResult, error) {
	res := ChainResult{Last: from}
	for {
		n, err := s.verifySlice(ctx, &res, pace.slice())
		if err != nil || res.BadID != 0 || n < pace.slice() {
			return res, err
		}
		if !pace.wait(ctx) {
			return res, ctx.Err()
		}
	}
}

// verifySlice checks the next limit records after res.Last, updating res, and
// returns how many it read.
func (s *Store) verifySlice(ctx context.Context, res *ChainResult, limit int) (int, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, COALESCE(ts, ''), source_file, record, prev_hash, hash FROM raw_log_records WHERE id > ? ORDER BY id LIMIT ?`, res.Last.ID, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id int64
		var ts, src, ph, h string
		var stored any
		if err := rows.Scan(&id, &ts, &src, &stored, &ph, &h); err != nil {
			return n, err
		}
		n++
		res.Checked++
		rec, err := DecodeRecord(stored)
		if err != nil {
			res.BadID = id
			return n, nil
		}
		if ph != res.Last.Hash || ChainHash(res.Last.Hash, ts, src, rec) != h {
			res.BadID = id
			return n, nil
		}
		res.Last = ChainPoint{ID: id, Hash: h}
	}
	return n, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The queries the merge runs to tie a logged file tool call (Read, Edit, Write)
// to what the kernel saw the agent process do. These tools run inside the agent
// process, so there is no exec to pair with: the kernel reports the process
// opening a file (a read) or, for a write, creating a temp file next to it and
// renaming it over the target (`<path>.tmp.<pid>.<hash>`). See
// docs/tetragon-integration.md "Built-in tools".

// FileRef is a logged file action the merge has not confirmed yet.
type FileRef struct {
	ActionID  int64
	ToolUseID string
	SessionID int64
	TS        string // when the agent issued the call
	ResultTS  string // when its result was logged ("" = not yet)
	PromptEnd string // when its prompt ended ("" = still running)
	Action    string // read | update | create
	Path      string
}

// OpenFileActions lists logged file actions the kernel has not confirmed, that
// the merge has more to do with: the call is not settled, or an older matcher
// settled it.
func (t *Tx) OpenFileActions(ctx context.Context, matcher int) ([]FileRef, error) {
	rows, err := t.tx.QueryContext(ctx, `
SELECT a.id, a.tool_use_id, a.session_id, a.ts, COALESCE(a.result_ts, ''), COALESCE(p.ended_at, ''), a.action, a.path
FROM actions a
LEFT JOIN prompts p ON p.id = a.prompt_id
LEFT JOIN call_links l ON l.tool_use_id = a.tool_use_id
WHERE a.resource = 'file' AND a.action IN ('read', 'update', 'create') AND a.evidence = 'log'
  AND a.tool_use_id IS NOT NULL AND a.session_id IS NOT NULL AND a.path IS NOT NULL
  AND (COALESCE(l.settled, 0) = 0 OR COALESCE(l.matcher, 0) <> ?)
ORDER BY a.ts, a.id`, matcher)
	if err != nil {
		return nil, fmt.Errorf("open file actions: %w", err)
	}
	defer rows.Close()
	var out []FileRef
	for rows.Next() {
		var r FileRef
		if err := rows.Scan(&r.ActionID, &r.ToolUseID, &r.SessionID, &r.TS, &r.ResultTS, &r.PromptEnd, &r.Action, &r.Path); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AgentPIDs lists the pids of the agent processes attributed to a session.
func (t *Tx) AgentPIDs(ctx context.Context, session int64) ([]int64, error) {
	rows, err := t.tx.QueryContext(ctx, `
SELECT DISTINCT p.pid FROM process_attribution a JOIN processes p ON p.exec_id = a.exec_id
WHERE a.session_id = ? AND a.exec_id = a.root_exec_id`, session)
	if err != nil {
		return nil, fmt.Errorf("agent pids: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var pid int64
		if err := rows.Scan(&pid); err != nil {
			return nil, err
		}
		out = append(out, pid)
	}
	return out, rows.Err()
}

// FileEvent finds the earliest kernel event, between from and to, that an agent
// process (one of pids) made for a file action and no logged action has claimed:
// for a read, opening path; for a write, renaming a temp file over path, or
// creating path. It returns 0 when there is none.
func (t *Tx) FileEvent(ctx context.Context, pids []int64, action, path, from, to string) (int64, error) {
	if len(pids) == 0 {
		return 0, nil
	}
	ph := ""
	args := []any{from, to}
	for i, pid := range pids {
		if i > 0 {
			ph += ","
		}
		ph += "?"
		args = append(args, pid)
	}
	var cond string
	if action == "read" {
		cond = `e.type = 'file_open' AND e.path = ?`
		args = append(args, path)
	} else {
		// the temp file is `<path>.tmp.<pid>.<hash>`, renamed over path
		cond = `((e.type = 'file_rename' AND substr(e.path, 1, length(?)) = ? AND substr(e.path, length(?) + 1, 5) = '.tmp.')
		         OR (e.type = 'file_create' AND e.path = ?))`
		args = append(args, path, path, path, path)
	}
	var id int64
	err := t.tx.QueryRowContext(ctx, `
SELECT e.id FROM kernel_events e JOIN processes p ON p.exec_id = e.exec_id
WHERE e.ts >= ? AND e.ts <= ? AND p.pid IN (`+ph+`) AND `+cond+`
  AND NOT EXISTS (SELECT 1 FROM actions x WHERE x.kernel_event_id = e.id)
ORDER BY e.ts, e.id LIMIT 1`, args...).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("file event: %w", err)
	}
	return id, nil
}

// LinkFile says the kernel saw a logged file action: the action is now seen by both.
func (t *Tx) LinkFile(ctx context.Context, actionID, eventID int64) error {
	_, err := t.tx.ExecContext(ctx, `UPDATE actions SET evidence = 'both', kernel_event_id = ? WHERE id = ?`, eventID, actionID)
	if err != nil {
		return fmt.Errorf("link file: %w", err)
	}
	return nil
}

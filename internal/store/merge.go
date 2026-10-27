package store

import (
	"context"
	"fmt"
)

// The queries the merge (internal/merge) runs to tie the log's commands to
// kernel execs. See docs/tetragon-integration.md "Merging the log's commands
// with kernel execs".

// PartRef is one simple command of a Bash call.
type PartRef struct {
	ActionID int64
	Text     string // the simple command, as the log split it
	ExecID   string // "" until merged with a kernel exec
}

// CallRef is a Bash tool call the merge has not finished with.
type CallRef struct {
	ToolUseID string
	SessionID int64
	TS        string // when the agent issued it
	ResultTS  string // when its result was logged ("" = not yet)
	PromptEnd string // when its prompt ended ("" = still running)
	Command   string // the whole command line
	Shell     string // exec_id of the wrapper shell once matched
	Parts     []PartRef
}

// OpenCalls lists Bash calls that are not settled, oldest first, with their parts.
func (t *Tx) OpenCalls(ctx context.Context) ([]CallRef, error) {
	rows, err := t.tx.QueryContext(ctx, `
SELECT a.tool_use_id, a.session_id, a.ts, COALESCE(a.result_ts, ''), COALESCE(p.ended_at, ''), a.call_command, COALESCE(l.shell_exec_id, '')
FROM actions a
LEFT JOIN prompts p ON p.id = a.prompt_id
LEFT JOIN call_links l ON l.tool_use_id = a.tool_use_id
WHERE a.part = 0 AND a.call_command IS NOT NULL AND a.session_id IS NOT NULL AND COALESCE(l.settled, 0) = 0
ORDER BY a.ts, a.id`)
	if err != nil {
		return nil, fmt.Errorf("open calls: %w", err)
	}
	var calls []CallRef
	for rows.Next() {
		var c CallRef
		if err := rows.Scan(&c.ToolUseID, &c.SessionID, &c.TS, &c.ResultTS, &c.PromptEnd, &c.Command, &c.Shell); err != nil {
			rows.Close()
			return nil, err
		}
		calls = append(calls, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range calls {
		pr, err := t.tx.QueryContext(ctx, `
SELECT id, COALESCE(path, ''), COALESCE(exec_id, '') FROM actions WHERE tool_use_id = ? ORDER BY part`, calls[i].ToolUseID)
		if err != nil {
			return nil, fmt.Errorf("call parts: %w", err)
		}
		for pr.Next() {
			var p PartRef
			if err := pr.Scan(&p.ActionID, &p.Text, &p.ExecID); err != nil {
				pr.Close()
				return nil, err
			}
			calls[i].Parts = append(calls[i].Parts, p)
		}
		pr.Close()
		if err := pr.Err(); err != nil {
			return nil, err
		}
	}
	return calls, nil
}

// ProcRef is a process as the merge needs it.
type ProcRef struct {
	ExecID    string
	Binary    string
	Argv      string // JSON array
	Cwd       string
	StartedAt string
	SessionID int64
	PromptID  int64
}

// FreeShells lists attributed processes that look like the shell Claude Code
// wraps a Bash call in (an eval of the command) and are not yet tied to a call.
func (t *Tx) FreeShells(ctx context.Context) ([]ProcRef, error) {
	return t.procs(ctx, `
SELECT p.exec_id, p.binary, COALESCE(p.argv, ''), COALESCE(p.cwd, ''), p.started_at, a.session_id, COALESCE(a.prompt_id, 0)
FROM processes p JOIN process_attribution a ON a.exec_id = p.exec_id
WHERE p.argv LIKE '%eval%'
  AND NOT EXISTS (SELECT 1 FROM call_links l WHERE l.shell_exec_id = p.exec_id)
  AND NOT EXISTS (SELECT 1 FROM actions x WHERE x.exec_id = p.exec_id)
ORDER BY p.started_at`)
}

// Children lists the processes a process started directly, in start order.
func (t *Tx) Children(ctx context.Context, parent string) ([]ProcRef, error) {
	return t.procs(ctx, `
SELECT p.exec_id, p.binary, COALESCE(p.argv, ''), COALESCE(p.cwd, ''), p.started_at, 0, 0
FROM processes p WHERE p.parent_exec_id = ?
  AND NOT EXISTS (SELECT 1 FROM actions x WHERE x.exec_id = p.exec_id)
ORDER BY p.started_at, p.exec_id`, parent)
}

// Unexplained lists attributed processes that no log action accounts for and
// that the merge may turn into kernel actions: the direct children of a
// matched wrapper shell, and of an agent's root process (its routine commands
// and any wrapper no call matched). A process in a session whose log has not
// reached its start yet is left for a later pass, so a call logged late can
// still claim it.
func (t *Tx) Unexplained(ctx context.Context) ([]ProcRef, error) {
	return t.procs(ctx, `
SELECT p.exec_id, p.binary, COALESCE(p.argv, ''), COALESCE(p.cwd, ''), p.started_at, a.session_id, COALESCE(a.prompt_id, 0)
FROM processes p
JOIN process_attribution a ON a.exec_id = p.exec_id
JOIN sessions s ON s.id = a.session_id
WHERE s.ended_at >= p.started_at
  AND NOT EXISTS (SELECT 1 FROM actions x WHERE x.exec_id = p.exec_id)
  AND NOT EXISTS (SELECT 1 FROM call_links l WHERE l.shell_exec_id = p.exec_id)
  AND (EXISTS (SELECT 1 FROM call_links l WHERE l.shell_exec_id = p.parent_exec_id)
       OR (EXISTS (SELECT 1 FROM process_attribution q WHERE q.exec_id = p.parent_exec_id)
           AND NOT EXISTS (SELECT 1 FROM processes par JOIN process_attribution r ON r.exec_id = par.parent_exec_id
                           WHERE par.exec_id = p.parent_exec_id)))
ORDER BY p.started_at`)
}

func (t *Tx) procs(ctx context.Context, q string, args ...any) ([]ProcRef, error) {
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("processes: %w", err)
	}
	defer rows.Close()
	var out []ProcRef
	for rows.Next() {
		var p ProcRef
		if err := rows.Scan(&p.ExecID, &p.Binary, &p.Argv, &p.Cwd, &p.StartedAt, &p.SessionID, &p.PromptID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// KernelRange is the first and last kernel event times ("" when there are none).
func (t *Tx) KernelRange(ctx context.Context) (first, last string, err error) {
	var f, l *string
	err = t.tx.QueryRowContext(ctx, `SELECT MIN(ts), MAX(ts) FROM kernel_events`).Scan(&f, &l)
	if f != nil {
		first = *f
	}
	if l != nil {
		last = *l
	}
	return first, last, err
}

// LinkCall records which wrapper shell ran a call.
func (t *Tx) LinkCall(ctx context.Context, toolUseID, shell string) error {
	_, err := t.tx.ExecContext(ctx, `
INSERT INTO call_links (tool_use_id, shell_exec_id) VALUES (?, ?)
ON CONFLICT (tool_use_id) DO UPDATE SET shell_exec_id = excluded.shell_exec_id`, toolUseID, shell)
	if err != nil {
		return fmt.Errorf("link call: %w", err)
	}
	return nil
}

// SettleCall says no later kernel event can change a call's links.
func (t *Tx) SettleCall(ctx context.Context, toolUseID string) error {
	_, err := t.tx.ExecContext(ctx, `
INSERT INTO call_links (tool_use_id, settled) VALUES (?, 1)
ON CONFLICT (tool_use_id) DO UPDATE SET settled = 1`, toolUseID)
	if err != nil {
		return fmt.Errorf("settle call: %w", err)
	}
	return nil
}

// LinkPart ties a logged command to the kernel exec that ran it: the action is
// now seen by both.
func (t *Tx) LinkPart(ctx context.Context, actionID int64, execID string) error {
	_, err := t.tx.ExecContext(ctx, `UPDATE actions SET exec_id = ?, evidence = 'both' WHERE id = ?`, execID, actionID)
	if err != nil {
		return fmt.Errorf("link part: %w", err)
	}
	return nil
}

// AddKernelAction records a command only the kernel saw. It does not count
// toward the prompt's action total (views group these routine runs; see
// docs/tetragon-integration.md).
func (t *Tx) AddKernelAction(ctx context.Context, a Action) error {
	_, err := t.tx.ExecContext(ctx, `
INSERT INTO actions (session_id, prompt_id, exec_id, ts, resource, action, path, evidence)
VALUES (NULLIF(?, 0), NULLIF(?, 0), ?, ?, ?, ?, NULLIF(?, ''), 'kernel')
ON CONFLICT DO NOTHING`, a.SessionID, a.PromptID, a.ExecID, a.TS, a.Resource, a.Action, a.Path)
	if err != nil {
		return fmt.Errorf("add kernel action: %w", err)
	}
	return nil
}

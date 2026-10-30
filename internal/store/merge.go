package store

import (
	"context"
	"fmt"

	"github.com/nosaka4i/sentient/internal/background"
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

// OpenCalls lists the Bash calls the merge has more to do with, oldest first,
// with their parts: the ones not settled, and the ones settled by an older
// version of the matching (a better matcher gets another look at calls the old
// one gave up on).
func (t *Tx) OpenCalls(ctx context.Context, matcher int) ([]CallRef, error) {
	rows, err := t.tx.QueryContext(ctx, `
SELECT a.tool_use_id, a.session_id, a.ts, COALESCE(a.result_ts, ''), COALESCE(p.ended_at, ''), a.call_command, COALESCE(l.shell_exec_id, '')
FROM actions a
LEFT JOIN prompts p ON p.id = a.prompt_id
LEFT JOIN call_links l ON l.tool_use_id = a.tool_use_id
WHERE a.part = 0 AND a.call_command IS NOT NULL AND a.session_id IS NOT NULL
  AND (COALESCE(l.settled, 0) = 0 OR COALESCE(l.matcher, 0) <> ?)
ORDER BY a.ts, a.id`, matcher)
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

// Children lists the commands a shell ran, in start order: the processes it
// started directly, and, through the subshells it forked for pipelines and
// groups (see notFork), what those ran. A fork is not itself a command.
func (t *Tx) Children(ctx context.Context, parent string) ([]ProcRef, error) {
	return t.procs(ctx, `
WITH RECURSIVE kids(exec_id) AS (
  SELECT exec_id FROM processes WHERE parent_exec_id = ?
  UNION
  SELECT c.exec_id FROM kids k
  JOIN processes f ON f.exec_id = k.exec_id
  JOIN processes c ON c.parent_exec_id = f.exec_id
  WHERE NOT `+notForkOf("f")+`
)
SELECT p.exec_id, p.binary, COALESCE(p.argv, ''), COALESCE(p.cwd, ''), p.started_at, 0, 0
FROM kids JOIN processes p ON p.exec_id = kids.exec_id
WHERE NOT EXISTS (SELECT 1 FROM actions x WHERE x.exec_id = p.exec_id AND x.evidence <> 'kernel')
  AND `+notFork+`
  AND NOT `+replaced+`
ORDER BY p.started_at, p.exec_id`, parent)
}

// notForkOf is the condition (on the processes row aliased a) that a process is
// not a fork of its parent: a shell forks subshells for pipelines and Tetragon
// reports each as an exec of the same binary with the same arguments. A fork is
// the parent running on, not a command it started.
func notForkOf(a string) string {
	return `NOT EXISTS (SELECT 1 FROM processes par WHERE par.exec_id = ` + a + `.parent_exec_id AND par.binary = ` + a + `.binary AND COALESCE(par.argv, '') = COALESCE(` + a + `.argv, ''))`
}

var notFork = notForkOf("p")

// replaced is the condition (on processes p) that a later exec of the same pid
// and the same parent took the process over, within moments. A process starts
// as a copy of its parent (the collector reports it as an exec of the parent's
// binary) before it execs the program it runs, so the copy is never a command;
// the exec that replaced it is. A pipeline's subshell is replaced the same way.
const replaced = `EXISTS (SELECT 1 FROM processes nx
  WHERE nx.pid = p.pid AND nx.exec_id <> p.exec_id AND nx.parent_exec_id IS p.parent_exec_id
    AND nx.started_at > p.started_at AND julianday(nx.started_at) - julianday(p.started_at) < 5.0 / 86400)`

// UncheckedBackground lists the direct children of agent roots that have not
// been checked against the background rules of the given version yet. Stubs
// (see replaced) are not offered; MarkStubs settles them.
func (t *Tx) UncheckedBackground(ctx context.Context, rules int) ([]ProcRef, error) {
	return t.procs(ctx, `
SELECT p.exec_id, p.binary, COALESCE(p.argv, ''), COALESCE(p.cwd, ''), p.started_at, a.session_id, COALESCE(a.prompt_id, 0)
FROM processes p JOIN process_attribution a ON a.exec_id = p.exec_id
WHERE (a.background IS NULL OR COALESCE(a.background_rules, 0) <> ?) AND `+agentChild+`
  AND NOT `+replaced+`
ORDER BY p.started_at`, rules)
}

// MarkStubs records that the agent's children that were replaced by a later
// exec (see replaced) are not background commands, under the given rules.
//
// It also settles the processes that are not an agent's direct children at all
// (a command's own subprocesses), so each pass only looks at what is new.
func (t *Tx) MarkStubs(ctx context.Context, rules int) error {
	const unchecked = `(a.background IS NULL OR COALESCE(a.background_rules, 0) <> ?)`
	if _, err := t.tx.ExecContext(ctx, `
UPDATE process_attribution SET background = '', background_rules = ?
WHERE exec_id IN (SELECT p.exec_id FROM process_attribution a JOIN processes p ON p.exec_id = a.exec_id
                  WHERE `+unchecked+` AND NOT `+agentChild+`)`, rules, rules); err != nil {
		return fmt.Errorf("mark non-children: %w", err)
	}
	if _, err := t.tx.ExecContext(ctx, `
UPDATE process_attribution SET background = '', background_rules = ?
WHERE exec_id IN (SELECT p.exec_id FROM process_attribution a JOIN processes p ON p.exec_id = a.exec_id
                  WHERE `+unchecked+` AND `+agentChild+` AND `+replaced+`)`, rules, rules); err != nil {
		return fmt.Errorf("mark stubs: %w", err)
	}
	return nil
}

// ToolRuns lists the redirected grep and find runs (see background.IsToolRun)
// that fit inside a wrapper shell: the collector records them with no parent,
// so only the shell's lifetime and directory tie them to it.
func (t *Tx) ToolRuns(ctx context.Context, shell string) ([]ProcRef, error) {
	like := ""
	var args []any
	args = append(args, shell)
	for _, pre := range background.ToolRunPrefixes() {
		if like != "" {
			like += " OR "
		}
		like += "p.argv LIKE ?"
		args = append(args, pre+"%")
	}
	return t.procs(ctx, `
SELECT p.exec_id, p.binary, COALESCE(p.argv, ''), COALESCE(p.cwd, ''), p.started_at, 0, 0
FROM processes s
JOIN processes p ON p.cwd = s.cwd AND p.started_at >= s.started_at AND (s.exited_at IS NULL OR p.started_at <= s.exited_at)
WHERE s.exec_id = ? AND COALESCE(p.parent_exec_id, '') = '' AND (`+like+`)
  AND NOT EXISTS (SELECT 1 FROM actions x WHERE x.exec_id = p.exec_id AND x.evidence <> 'kernel')
ORDER BY p.started_at, p.exec_id`, args...)
}

// agentChild is the condition (on processes p and its attribution a) for a
// direct child of an agent process: its parent is the agent's root, or a later
// exec of the same process (the agent restarted itself in place: same pid, a
// new exec, with the old one as its parent).
const agentChild = `(p.parent_exec_id = a.root_exec_id
  OR EXISTS (SELECT 1 FROM processes par JOIN processes r ON r.exec_id = a.root_exec_id
             WHERE par.exec_id = p.parent_exec_id AND par.pid = r.pid))`

// SetBackground records the result of checking a process against the
// background rules of the given version: the group key of its command, or ""
// when it is not one.
func (t *Tx) SetBackground(ctx context.Context, execID, key string, rules int) error {
	_, err := t.tx.ExecContext(ctx, `UPDATE process_attribution SET background = ?, background_rules = ? WHERE exec_id = ?`, key, rules, execID)
	if err != nil {
		return fmt.Errorf("set background: %w", err)
	}
	return nil
}

// DropBackgroundActions removes kernel-only actions for processes now known to
// be background commands, forks of their parent or replaced stubs (they were
// recorded before the rules knew them). An action an alert points at is kept.
func (t *Tx) DropBackgroundActions(ctx context.Context) (int64, error) {
	res, err := t.tx.ExecContext(ctx, `
DELETE FROM actions WHERE evidence = 'kernel' AND exec_id IN
  (SELECT exec_id FROM process_attribution WHERE COALESCE(background, '') <> '')
  AND NOT EXISTS (SELECT 1 FROM alerts WHERE alerts.action_id = actions.id)`)
	if err != nil {
		return 0, fmt.Errorf("drop background actions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return n, err
	}
	res, err = t.tx.ExecContext(ctx, `
DELETE FROM actions WHERE evidence = 'kernel'
  AND (EXISTS (SELECT 1 FROM processes p JOIN processes par ON par.exec_id = p.parent_exec_id
               WHERE p.exec_id = actions.exec_id AND par.binary = p.binary AND COALESCE(par.argv, '') = COALESCE(p.argv, ''))
       OR EXISTS (SELECT 1 FROM processes p WHERE p.exec_id = actions.exec_id AND `+replaced+`))
  AND NOT EXISTS (SELECT 1 FROM alerts WHERE alerts.action_id = actions.id)`)
	if err != nil {
		return n, fmt.Errorf("drop fork actions: %w", err)
	}
	m, err := res.RowsAffected()
	return n + m, err
}

// Unexplained lists attributed processes that no log action accounts for and
// that the merge may turn into kernel actions: the direct children of a
// matched wrapper shell, and of an agent's root process (any wrapper no call
// matched, and commands nothing explains; known background commands are not
// actions). A process in a session whose log has not reached its start yet is
// left for a later pass, so a call logged late can still claim it.
func (t *Tx) Unexplained(ctx context.Context) ([]ProcRef, error) {
	return t.procs(ctx, `
SELECT p.exec_id, p.binary, COALESCE(p.argv, ''), COALESCE(p.cwd, ''), p.started_at, a.session_id, COALESCE(a.prompt_id, 0)
FROM processes p
JOIN process_attribution a ON a.exec_id = p.exec_id
JOIN sessions s ON s.id = a.session_id
WHERE s.ended_at >= p.started_at
  AND COALESCE(a.background, '') = ''
  AND NOT EXISTS (SELECT 1 FROM actions x WHERE x.exec_id = p.exec_id)
  AND NOT EXISTS (SELECT 1 FROM call_links l WHERE l.shell_exec_id = p.exec_id)
  AND `+notFork+`
  AND NOT `+replaced+`
  AND (EXISTS (SELECT 1 FROM call_links l WHERE l.shell_exec_id = p.parent_exec_id)
       OR `+agentChild+`)
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
	// two subqueries, not MIN and MAX together: SQLite answers each from the ts
	// index, but scans the whole table for the pair
	err = t.tx.QueryRowContext(ctx, `SELECT (SELECT MIN(ts) FROM kernel_events), (SELECT MAX(ts) FROM kernel_events)`).Scan(&f, &l)
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

// SettleCall says no later kernel event can change a call's links, under the
// given version of the matching.
func (t *Tx) SettleCall(ctx context.Context, toolUseID string, matcher int) error {
	_, err := t.tx.ExecContext(ctx, `
INSERT INTO call_links (tool_use_id, settled, matcher) VALUES (?, 1, ?)
ON CONFLICT (tool_use_id) DO UPDATE SET settled = 1, matcher = excluded.matcher`, toolUseID, matcher)
	if err != nil {
		return fmt.Errorf("settle call: %w", err)
	}
	return nil
}

// LinkPart ties a logged command to the kernel exec that ran it: the action is
// now seen by both.
func (t *Tx) LinkPart(ctx context.Context, actionID int64, execID string) error {
	// the exec may already be a kernel-only action (recorded before the log
	// explained it): the logged command now accounts for it, so it is not also
	// an unreported one. An action an alert points at is kept (the update then
	// fails on the unique exec).
	if _, err := t.tx.ExecContext(ctx, `
DELETE FROM actions WHERE exec_id = ? AND evidence = 'kernel'
  AND NOT EXISTS (SELECT 1 FROM alerts WHERE alerts.action_id = actions.id)`, execID); err != nil {
		return fmt.Errorf("link part: %w", err)
	}
	_, err := t.tx.ExecContext(ctx, `UPDATE actions SET exec_id = ?, evidence = 'both' WHERE id = ?`, execID, actionID)
	if err != nil {
		return fmt.Errorf("link part: %w", err)
	}
	return nil
}

// AddKernelAction records a command only the kernel saw. It does not count
// toward the prompt's action total (see docs/tetragon-integration.md).
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

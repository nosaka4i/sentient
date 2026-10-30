package store

import (
	"context"
	"fmt"
)

// ProcessRef is an unattributed process, with the binary of its parent so the
// caller can tell an agent's root process from its helpers.
type ProcessRef struct {
	ExecID       string
	Binary       string
	Argv         string // JSON array
	Cwd          string
	StartedAt    string
	ExitedAt     string // "" while running
	ParentBinary string // "" when the parent was not recorded
}

// UnattributedProcesses lists processes not yet tied to a session.
func (t *Tx) UnattributedProcesses(ctx context.Context) ([]ProcessRef, error) {
	rows, err := t.tx.QueryContext(ctx, `
SELECT p.exec_id, p.binary, COALESCE(p.argv, ''), COALESCE(p.cwd, ''), p.started_at, COALESCE(p.exited_at, ''), COALESCE(q.binary, '')
FROM processes p LEFT JOIN processes q ON q.exec_id = p.parent_exec_id
WHERE NOT EXISTS (SELECT 1 FROM process_attribution a WHERE a.exec_id = p.exec_id)`)
	if err != nil {
		return nil, fmt.Errorf("unattributed processes: %w", err)
	}
	defer rows.Close()
	var out []ProcessRef
	for rows.Next() {
		var r ProcessRef
		if err := rows.Scan(&r.ExecID, &r.Binary, &r.Argv, &r.Cwd, &r.StartedAt, &r.ExitedAt, &r.ParentBinary); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionHit is a session that had log activity in a time window.
type SessionHit struct {
	SessionID int64
	Started   string // when the session began (its first record, ever)
}

// SessionsActive returns the sessions started in cwd that wrote log records
// between from and to, the one that began first first.
func (t *Tx) SessionsActive(ctx context.Context, cwd, from, to string) ([]SessionHit, error) {
	rows, err := t.tx.QueryContext(ctx, `
SELECT r.session_id, COALESCE(s.started_at, '') FROM raw_log_records r JOIN sessions s ON s.id = r.session_id
WHERE s.cwd = ? AND r.ts >= ? AND r.ts <= ?
GROUP BY r.session_id ORDER BY s.started_at, r.session_id`, cwd, from, to)
	if err != nil {
		return nil, fmt.Errorf("sessions active: %w", err)
	}
	defer rows.Close()
	var out []SessionHit
	for rows.Next() {
		var h SessionHit
		if err := rows.Scan(&h.SessionID, &h.Started); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// AttributeRoot ties an agent's root process to a session. how is "exact" or
// "ambiguous".
func (t *Tx) AttributeRoot(ctx context.Context, execID string, sessionID int64, how string) error {
	_, err := t.tx.ExecContext(ctx, `
INSERT INTO process_attribution (exec_id, session_id, attribution, root_exec_id) VALUES (?, ?, ?, ?)
ON CONFLICT (exec_id) DO UPDATE SET session_id = excluded.session_id, attribution = excluded.attribution`,
		execID, sessionID, how, execID)
	if err != nil {
		return fmt.Errorf("attribute root: %w", err)
	}
	return nil
}

// maxProcessDepth bounds how deep a process tree is followed in one pass.
const maxProcessDepth = 64

// AttributeDescendants gives every process whose parent has a session that
// session, and the prompt in effect when it started, level by level. It
// returns how many processes it attributed.
func (t *Tx) AttributeDescendants(ctx context.Context) (int, error) {
	total := 0
	for i := 0; i < maxProcessDepth; i++ {
		res, err := t.tx.ExecContext(ctx, `
INSERT INTO process_attribution (exec_id, session_id, prompt_id, attribution, root_exec_id)
SELECT p.exec_id, q.session_id,
  (SELECT pr.id FROM prompts pr
   WHERE pr.session_id = q.session_id AND pr.started_at <= p.started_at
   ORDER BY pr.started_at DESC, pr.seq DESC LIMIT 1),
  q.attribution, q.root_exec_id
FROM processes p JOIN process_attribution q ON q.exec_id = p.parent_exec_id
WHERE NOT EXISTS (SELECT 1 FROM process_attribution a WHERE a.exec_id = p.exec_id)`)
		if err != nil {
			return total, fmt.Errorf("attribute descendants: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		if n == 0 {
			break
		}
		total += int(n)
	}
	return total, nil
}

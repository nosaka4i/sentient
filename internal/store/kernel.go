package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Process is a process a collector saw start: the agent's tree.
type Process struct {
	ExecID       string // unique per exec, from the collector
	Collector    string // tetragon | eslogger
	PID          int64
	ParentExecID string
	Binary       string
	Argv         string // JSON array
	Cwd          string
	UID          int64
	StartedAt    string
}

// AddProcess records a process. A process already recorded under the same
// exec_id is left as it is (a collector may report an exec twice, e.g. once
// from the /proc scan and again live).
func (t *Tx) AddProcess(ctx context.Context, p Process) error {
	_, err := t.tx.ExecContext(ctx, `
INSERT INTO processes (exec_id, collector, pid, parent_exec_id, binary, argv, cwd, uid, started_at)
VALUES (?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?)
ON CONFLICT (exec_id) DO NOTHING`,
		p.ExecID, p.Collector, p.PID, p.ParentExecID, p.Binary, p.Argv, p.Cwd, p.UID, p.StartedAt)
	if err != nil {
		return fmt.Errorf("add process: %w", err)
	}
	return nil
}

// ExitProcess records when a process ended and its exit status: an `exit`
// event appended to the kernel chain, and the processes index updated from it.
// Exiting a process that was never recorded is not an error (the exec may
// predate the collector); it reports whether the process was known.
func (t *Tx) ExitProcess(ctx context.Context, execID, at string, code int) (bool, error) {
	if _, err := t.AppendKernel(ctx, KernelEvent{ExecID: execID, TS: at, Type: "exit", Payload: fmt.Sprintf(`{"exit_code":%d}`, code)}); err != nil {
		return false, err
	}
	res, err := t.tx.ExecContext(ctx, `UPDATE processes SET exited_at = ?, exit_code = ? WHERE exec_id = ?`, at, code, execID)
	if err != nil {
		return false, fmt.Errorf("exit process: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// AttributeProcess ties a process to an agent session and prompt. Zero for
// either leaves it as it was.
func (t *Tx) AttributeProcess(ctx context.Context, execID string, sessionID, promptID int64) error {
	_, err := t.tx.ExecContext(ctx, `
INSERT INTO process_attribution (exec_id, session_id, prompt_id) VALUES (?, ?, NULLIF(?, 0))
ON CONFLICT (exec_id) DO UPDATE SET session_id = COALESCE(NULLIF(?, 0), session_id), prompt_id = COALESCE(NULLIF(?, 0), prompt_id)`,
		execID, sessionID, promptID, sessionID, promptID)
	if err != nil {
		return fmt.Errorf("attribute process: %w", err)
	}
	return nil
}

// KernelEvent is one thing a process did, as a collector reported it.
type KernelEvent struct {
	ExecID  string
	TS      string
	Type    string // exec | exit | file_create | file_write | file_unlink | file_rename | file_setmode | net_connect | kill | ...
	Path    string
	Path2   string // rename target
	Payload string // the collector's record, verbatim JSON
}

// kernelSource is what a kernel event's hash covers besides its time and
// payload, so changing the process, type or paths breaks the chain too.
func kernelSource(execID, typ, path, path2 string) string {
	return execID + "\x00" + typ + "\x00" + path + "\x00" + path2
}

// AppendKernel appends an event to the kernel chain and returns its id.
func (t *Tx) AppendKernel(ctx context.Context, e KernelEvent) (int64, error) {
	var prev string
	err := t.tx.QueryRowContext(ctx, `SELECT hash FROM kernel_events ORDER BY id DESC LIMIT 1`).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("read kernel chain head: %w", err)
	}
	hash := ChainHash(prev, e.TS, kernelSource(e.ExecID, e.Type, e.Path, e.Path2), e.Payload)
	res, err := t.tx.ExecContext(ctx, `
INSERT INTO kernel_events (exec_id, ts, type, path, path2, payload, prev_hash, hash)
VALUES (NULLIF(?, ''), ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)`,
		e.ExecID, e.TS, e.Type, e.Path, e.Path2, e.Payload, prev, hash)
	if err != nil {
		return 0, fmt.Errorf("append kernel event: %w", err)
	}
	return res.LastInsertId()
}

// VerifyKernelChainFrom checks the kernel events after `from`, like
// VerifyChainFrom does for raw log records.
func (s *Store) VerifyKernelChainFrom(ctx context.Context, from ChainPoint) (ChainResult, error) {
	res := ChainResult{Last: from}
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, COALESCE(exec_id, ''), ts, type, COALESCE(path, ''), COALESCE(path2, ''), payload, prev_hash, hash
FROM kernel_events WHERE id > ? ORDER BY id`, from.ID)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var exec, ts, typ, path, path2, payload, ph, h string
		if err := rows.Scan(&id, &exec, &ts, &typ, &path, &path2, &payload, &ph, &h); err != nil {
			return res, err
		}
		res.Checked++
		if ph != res.Last.Hash || ChainHash(res.Last.Hash, ts, kernelSource(exec, typ, path, path2), payload) != h {
			res.BadID = id
			return res, nil
		}
		res.Last = ChainPoint{ID: id, Hash: h}
	}
	return res, rows.Err()
}

// Gap is a stretch of time a collector wasn't watching.
type Gap struct {
	Collector string
	StartedAt string
	Reason    string
}

// OpenGap records that a collector stopped watching, and returns the gap's
// id; CloseGap ends it when the collector is back.
func (t *Tx) OpenGap(ctx context.Context, g Gap) (int64, error) {
	res, err := t.tx.ExecContext(ctx, `INSERT INTO monitor_gaps (collector, started_at, reason) VALUES (?, ?, ?)`,
		g.Collector, g.StartedAt, g.Reason)
	if err != nil {
		return 0, fmt.Errorf("open gap: %w", err)
	}
	return res.LastInsertId()
}

// CloseGap ends a gap. Closing an unknown or already closed gap changes
// nothing and reports false.
func (t *Tx) CloseGap(ctx context.Context, id int64, endedAt string) (bool, error) {
	res, err := t.tx.ExecContext(ctx, `UPDATE monitor_gaps SET ended_at = ? WHERE id = ? AND ended_at IS NULL`, endedAt, id)
	if err != nil {
		return false, fmt.Errorf("close gap: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

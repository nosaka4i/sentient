// Package merge ties the commands an agent's log says it ran to the execs a
// kernel collector saw (docs/tetragon-integration.md "Merging the log's
// commands with kernel execs").
//
// Claude Code runs each Bash call as a wrapper shell whose argument holds the
// whole command, so a call is matched to its shell by that text, not by time:
// the command runs only after any approval, which can take days. A call's
// parts then pair with the shell's direct children. What the kernel saw and the
// log did not becomes a kernel-only action.
package merge

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/store"
)

// slack allows for the log and the kernel disagreeing about the clock a little.
const slack = 5 * time.Second

// Merger merges in a store.
type Merger struct {
	St  *store.Store
	Log *slog.Logger
}

// Run merges every interval until ctx is cancelled.
func (m *Merger) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, err := m.Once(ctx); err != nil && ctx.Err() == nil && m.Log != nil {
				m.Log.Error("merge log and kernel", "err", err)
			}
		}
	}
}

// Once makes one pass and returns how many links and kernel actions it made.
func (m *Merger) Once(ctx context.Context) (int, error) {
	tx, err := m.St.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	first, last, err := tx.KernelRange(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	if last != "" {
		k, err := mergeCalls(ctx, tx, first, last)
		if err != nil {
			return 0, err
		}
		n += k
		k, err = kernelOnly(ctx, tx)
		if err != nil {
			return 0, err
		}
		n += k
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// mergeCalls matches open Bash calls to wrapper shells and pairs their parts
// with the shells' children, and settles the calls nothing can change any more.
func mergeCalls(ctx context.Context, tx *store.Tx, kernelFirst, kernelLast string) (int, error) {
	calls, err := tx.OpenCalls(ctx)
	if err != nil {
		return 0, err
	}
	if len(calls) == 0 {
		return 0, nil
	}
	shells, err := tx.FreeShells(ctx)
	if err != nil {
		return 0, err
	}
	payload := map[string]string{} // shell exec_id -> normalized command it runs
	for _, s := range shells {
		payload[s.ExecID] = norm(shellCommand(s.Argv))
	}
	taken := map[string]bool{}
	n := 0
	for _, c := range calls {
		if end := callEnd(c); end != "" && end < kernelFirst {
			// over before the kernel was being watched: nothing to find
			if err := tx.SettleCall(ctx, c.ToolUseID); err != nil {
				return n, err
			}
			continue
		}
		if c.Shell == "" {
			want := norm(c.Command)
			for _, s := range shells {
				if taken[s.ExecID] || s.SessionID != c.SessionID || payload[s.ExecID] != want || !startedIn(s.StartedAt, c) {
					continue
				}
				if err := tx.LinkCall(ctx, c.ToolUseID, s.ExecID); err != nil {
					return n, err
				}
				taken[s.ExecID] = true
				c.Shell = s.ExecID
				n++
				break
			}
		}
		if c.Shell != "" {
			k, err := pairParts(ctx, tx, c)
			if err != nil {
				return n, err
			}
			n += k
		}
		if end := callEnd(c); end != "" {
			// nothing the call started can still arrive once the kernel has
			// been seen past its end
			if t, err := dbtime.Parse(end); err == nil && dbtime.Format(t.Add(slack)) < kernelLast {
				if err := tx.SettleCall(ctx, c.ToolUseID); err != nil {
					return n, err
				}
			}
		}
	}
	return n, nil
}

// callEnd is when a call stopped being able to start processes: its result was
// logged, or its prompt ended (it was interrupted), whichever came first.
func callEnd(c store.CallRef) string {
	switch {
	case c.ResultTS == "":
		return c.PromptEnd
	case c.PromptEnd == "" || c.ResultTS < c.PromptEnd:
		return c.ResultTS
	}
	return c.PromptEnd
}

// startedIn says whether a shell that started at ts can be the one that ran c.
func startedIn(ts string, c store.CallRef) bool {
	start, err := dbtime.Parse(ts)
	if err != nil {
		return false
	}
	issued, err := dbtime.Parse(c.TS)
	if err != nil || start.Before(issued.Add(-slack)) {
		return false
	}
	if end := callEnd(c); end != "" {
		e, err := dbtime.Parse(end)
		if err != nil || start.After(e.Add(slack)) {
			return false
		}
	}
	return true
}

// pairParts pairs the parts of a call that have no exec yet with the direct
// children of its shell, in order, and returns how many it paired.
func pairParts(ctx context.Context, tx *store.Tx, c store.CallRef) (int, error) {
	kids, err := tx.Children(ctx, c.Shell)
	if err != nil {
		return 0, err
	}
	used := make([]bool, len(kids))
	n := 0
	for _, p := range c.Parts {
		if p.ExecID != "" {
			continue
		}
		want := norm(partCommand(p.Text))
		for i, k := range kids {
			if used[i] || norm(procCommand(k)) != want {
				continue
			}
			if err := tx.LinkPart(ctx, p.ActionID, k.ExecID); err != nil {
				return n, err
			}
			used[i] = true
			n++
			break
		}
	}
	return n, nil
}

// kernelOnly turns processes the log does not explain into actions.
func kernelOnly(ctx context.Context, tx *store.Tx) (int, error) {
	procs, err := tx.Unexplained(ctx)
	if err != nil {
		return 0, err
	}
	for _, p := range procs {
		text := procCommand(p)
		if cmd := shellCommand(p.Argv); cmd != "" {
			text = cmd // a wrapper no call matched: show the command it ran
		}
		err := tx.AddKernelAction(ctx, store.Action{
			SessionID: p.SessionID, PromptID: p.PromptID, ExecID: p.ExecID, TS: p.StartedAt,
			Resource: "command", Action: "execute", Path: text,
		})
		if err != nil {
			return 0, err
		}
	}
	return len(procs), nil
}

// norm makes two spellings of one command comparable: quoting differs between
// the log, the wrapper and the collector's argument splitting.
func norm(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\'', '"', '\\', ' ', '\t', '\n':
			return -1
		}
		return r
	}, s)
}

// argv decodes a process's JSON argument list; nothing on a bad value.
func argv(j string) []string {
	var a []string
	_ = json.Unmarshal([]byte(j), &a)
	return a
}

// procCommand is a process as one would type it: the program name and its arguments.
func procCommand(p store.ProcRef) string {
	return strings.TrimSpace(filepath.Base(p.Binary) + " " + strings.Join(argv(p.Argv), " "))
}

// shellCommand extracts the command a Claude Code wrapper shell evals from the
// shell's arguments ("" when it is not such a wrapper).
func shellCommand(argvJSON string) string {
	a := argv(argvJSON)
	if len(a) < 2 || a[0] != "-c" {
		return ""
	}
	s := strings.Join(a[1:], " ")
	const open = "eval "
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	s = s[i+len(open):]
	if j := strings.LastIndex(s, " < /dev/null"); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}

// partCommand drops leading VAR=value assignments and a directory in the
// program's path, leaving what procCommand shows for the process it starts.
func partCommand(part string) string {
	f := strings.Fields(part)
	for len(f) > 0 && isAssignment(f[0]) {
		f = f[1:]
	}
	if len(f) == 0 {
		return ""
	}
	f[0] = filepath.Base(f[0])
	return strings.Join(f, " ")
}

func isAssignment(w string) bool {
	i := strings.IndexByte(w, '=')
	if i <= 0 {
		return false
	}
	for j, r := range w[:i] {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || j > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// Package attribute ties the processes a kernel collector saw to the agent
// sessions and prompts in the log (docs/tetragon-integration.md "Tying a kernel
// event to a session and prompt").
//
// An agent's root process is matched to the session started in the same
// directory that was writing log records while the process ran. Every
// descendant belongs to its parent's session, and to the prompt in effect when
// it started. A root with no matching session yet stays unattributed and is
// tried again, because the log can lag behind the kernel.
package attribute

import (
	"context"
	"log/slog"
	"time"

	"github.com/nosaka4i/sentient/internal/background"
	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/store"
)

// Attribution values stored on processes.attribution.
const (
	Exact     = "exact"
	Ambiguous = "ambiguous"
)

// slack allows for the log and the kernel disagreeing about the clock a little.
const slack = 5 * time.Second

// minAgentLife is how long an agent process lives at least. The agent's binary
// also runs briefly on its own (the Grep tool re-runs it as ripgrep); with no
// recorded parent those runs look like agents, but they exit within a second.
const minAgentLife = 10 * time.Second

// bornWithin is how soon after an agent process starts its session's first
// record appears. A session that began later than that belongs to a later
// process, whatever its directory.
const bornWithin = time.Minute

// candidates narrows the sessions active in a root process's directory to the
// ones it can own. A session that began with the process is its own; only when
// there is none (the agent was started on an older session, e.g. resumed) do
// older sessions count.
func candidates(hits []store.SessionHit, start time.Time) []store.SessionHit {
	var born, older []store.SessionHit
	for _, h := range hits {
		began, err := dbtime.Parse(h.Started)
		switch {
		case err != nil || began.Before(start.Add(-slack)):
			older = append(older, h)
		case began.After(start.Add(bornWithin)):
			// belongs to a later process
		default:
			born = append(born, h)
		}
	}
	if len(born) > 0 {
		return born
	}
	return older
}

// Attributor attributes processes in a store.
type Attributor struct {
	St *store.Store
	// IsAgent says whether a binary path is a coding agent.
	IsAgent func(binary string) bool
	Log     *slog.Logger
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

// Run attributes every interval until ctx is cancelled.
func (a *Attributor) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, err := a.Once(ctx); err != nil && ctx.Err() == nil && a.Log != nil {
				a.Log.Error("attribute processes", "err", err)
			}
		}
	}
}

// Once makes one pass and returns how many processes it attributed.
func (a *Attributor) Once(ctx context.Context) (int, error) {
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	tx, err := a.St.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	procs, err := tx.UnattributedProcesses(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range procs {
		if !a.IsAgent(p.Binary) || (p.ParentBinary != "" && a.IsAgent(p.ParentBinary)) {
			continue // not an agent, or a helper the agent started: its parent decides
		}
		if background.IsToolRun(p.Argv) {
			continue // the agent's binary running a tool (ripgrep), not an agent
		}
		start, err := dbtime.Parse(p.StartedAt)
		if err != nil {
			continue
		}
		end := now()
		if p.ExitedAt != "" {
			if e, err := dbtime.Parse(p.ExitedAt); err == nil {
				end = e
				if e.Sub(start) < minAgentLife {
					continue // a one-off run of the binary (a helper, --version), not an agent session
				}
			}
		}
		hits, err := tx.SessionsActive(ctx, p.Cwd, dbtime.Format(start.Add(-slack)), dbtime.Format(end.Add(slack)))
		if err != nil {
			return 0, err
		}
		pool := candidates(hits, start)
		if len(pool) == 0 {
			continue
		}
		how := Exact
		if len(pool) > 1 {
			how = Ambiguous
		}
		if err := tx.AttributeRoot(ctx, p.ExecID, pool[0].SessionID, how); err != nil {
			return 0, err
		}
		n++
	}
	d, err := tx.AttributeDescendants(ctx)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n + d, nil
}

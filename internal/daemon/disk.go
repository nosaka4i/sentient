package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/dustin/go-humanize"
	"golang.org/x/sys/unix"

	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/store"
)

// DefaultDiskHigh is the share of the data directory's filesystem that may be
// used before recording pauses: the high threshold (docs/disk-thresholds.md).
const DefaultDiskHigh = 80

// lowBelowHigh is how far below the high threshold the low one is when it is
// not set: recording resumes there, so it does not flap around the high one.
const lowBelowHigh = 5

// diskCheckEvery is how often the disk is looked at.
const diskCheckEvery = 15 * time.Second

// closeStaleGaps ends the gaps the last run left open (a crash, or a stop while
// paused) at the time this run starts.
func closeStaleGaps(ctx context.Context, st *store.Store, now time.Time, log *slog.Logger) error {
	tx, err := st.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	n, err := tx.CloseOpenGaps(ctx, dbtime.Format(now))
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if n > 0 {
		log.Info("closed gaps the last run left open", "count", n)
	}
	return nil
}

// diskUsage is the free space available to sentientd, the size of the
// filesystem holding dir, and the share used. The share is what df prints:
// used over used plus available, rounded up, so blocks reserved for root count
// as neither (counting them as used would put it up to 5 points above df on a
// default ext4 disk).
func diskUsage(dir string) (free, size int64, usedPct int, err error) {
	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err != nil {
		return 0, 0, 0, err
	}
	bs := int64(fs.Bsize)
	return int64(fs.Bavail) * bs, int64(fs.Blocks) * bs, usedPercent(int64(fs.Blocks-fs.Bfree)*bs, int64(fs.Bavail)*bs), nil
}

// usedPercent is df's Use%: used / (used + avail), rounded up.
func usedPercent(used, avail int64) int {
	if used+avail <= 0 {
		return 0
	}
	return int((used*100 + used + avail - 1) / (used + avail))
}

// thresholds checks --disk-high-threshold and --disk-low-threshold and returns
// both: a low of 0 means "not set" and becomes lowBelowHigh under the high one
// (a high of 10 has no room for that, so its low is 1).
func thresholds(high, low int) (int, int, error) {
	if high == 0 {
		high = DefaultDiskHigh
	}
	if high < 10 || high > 99 {
		return 0, 0, fmt.Errorf("--disk-high-threshold must be between 10 and 99 (percent of the disk used)")
	}
	if low == 0 {
		low = max(high-lowBelowHigh, 1)
	}
	if low < 1 || low >= high {
		return 0, 0, fmt.Errorf("--disk-low-threshold must be between 1 and %d (below --disk-high-threshold %d)", high-1, high)
	}
	return high, low, nil
}

// diskGuard pauses recording when the disk is too full and resumes it when
// space comes back. Kernel events are dropped while paused, the transcript
// ingest, attribution and merge wait, and the pause is one gap in monitor_gaps.
// Nothing is deleted. Paused is what those parts ask.
type diskGuard struct {
	st     *store.Store
	dir    string
	high   int // pause at this share used
	low    int // resume at this share used
	statfs func(dir string) (free, size int64, usedPct int, err error)
	now    func() time.Time
	log    *slog.Logger

	mu     sync.Mutex
	paused bool
	since  time.Time
	gap    int64
	used   int
}

func newDiskGuard(st *store.Store, dir string, high, low int, log *slog.Logger) *diskGuard {
	return &diskGuard{st: st, dir: dir, high: high, low: low, statfs: diskUsage, now: time.Now, log: log}
}

// Paused reports whether recording is paused for lack of space.
func (g *diskGuard) Paused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused
}

// PausedSince is when the current pause began (zero when not paused).
func (g *diskGuard) PausedSince() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.paused {
		return time.Time{}
	}
	return g.since
}

// UsedPercent is the share of the disk used at the last check.
func (g *diskGuard) UsedPercent() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.used
}

// check looks at the disk once and pauses or resumes as needed.
func (g *diskGuard) check(ctx context.Context) {
	free, size, used, err := g.statfs(g.dir)
	if err != nil || size <= 0 {
		return // unknown: keep recording rather than stop on a failed measurement
	}
	g.mu.Lock()
	g.used = used
	paused := g.paused
	g.mu.Unlock()

	switch {
	case !paused && used >= g.high:
		g.pause(ctx, used, free)
	case paused && used <= g.low:
		g.resume(ctx, used)
	}
}

func (g *diskGuard) pause(ctx context.Context, used int, free int64) {
	now := g.now()
	reason := fmt.Sprintf("disk %d%% used: recording paused", used)
	id, err := g.openGap(ctx, now, reason)
	if err != nil {
		g.log.Error("cannot record the pause as a gap", "err", err)
	}
	g.mu.Lock()
	g.paused, g.since, g.gap = true, now, id
	g.mu.Unlock()
	g.log.Error(fmt.Sprintf("recording paused: disk %d%% used", used), "high", g.high, "low", g.low,
		"free", humanize.Bytes(uint64(free)), "hint", fmt.Sprintf("free space; recording resumes at %d%% used", g.low))
}

func (g *diskGuard) resume(ctx context.Context, used int) {
	now := g.now()
	g.mu.Lock()
	id, since := g.gap, g.since
	g.paused, g.gap = false, 0
	g.mu.Unlock()
	if id != 0 {
		if err := g.closeGap(ctx, id, now); err != nil {
			g.log.Error("cannot close the pause's gap", "err", err)
		}
	}
	g.log.Info("recording resumed", "used", used, "paused_for", now.Sub(since).Round(time.Second))
}

func (g *diskGuard) openGap(ctx context.Context, at time.Time, reason string) (int64, error) {
	tx, err := g.st.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := tx.OpenGap(ctx, store.Gap{Collector: "sentientd", StartedAt: dbtime.Format(at), Reason: reason})
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (g *diskGuard) closeGap(ctx context.Context, id int64, at time.Time) error {
	tx, err := g.st.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.CloseGap(ctx, id, dbtime.Format(at)); err != nil {
		return err
	}
	return tx.Commit()
}

// run checks until ctx is cancelled. The first check is made by the caller, so
// a start above the threshold is paused before anything is recorded.
func (g *diskGuard) run(ctx context.Context) {
	t := time.NewTicker(diskCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.check(ctx)
		}
	}
}

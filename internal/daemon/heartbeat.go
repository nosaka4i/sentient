package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/health"
	"github.com/nosaka4i/sentient/internal/store"
)

// heartbeatEvery is how often sentientd records that it is watching the kernel.
// After a crash the downtime gap starts up to this long early (docs/heartbeat.md).
const heartbeatEvery = 30 * time.Second

// heartbeatWait is how long one beat may wait for the database. The store has
// one connection, so a beat can queue behind a long write; it is dropped, not
// retried, and the next one goes out on schedule.
const heartbeatWait = 5 * time.Second

// heartbeat records that sentientd was running while a kernel collector was on,
// so the time sentientd itself was down becomes a gap on the next start. It is
// only used when a collector is started: without one a gap in kernel coverage
// means nothing (agent logs only mode shows no such marks).
type heartbeat struct {
	st        *store.Store
	collector string
	every     time.Duration // between beats
	wait      time.Duration // how long one beat may wait for the database
	now       func() time.Time
	log       *slog.Logger
}

func newHeartbeat(st *store.Store, collector string, log *slog.Logger) *heartbeat {
	return &heartbeat{st: st, collector: collector, every: heartbeatEvery, wait: heartbeatWait, now: time.Now, log: log}
}

// start settles what the last run left, then writes the first beat. staleClosed
// is how many open gaps the start just closed: they already cover the downtime,
// so no second gap is written over them.
func (h *heartbeat) start(ctx context.Context, staleClosed int64) error {
	now := h.now()
	prev, ok, err := h.st.Heartbeat(ctx, h.collector)
	if err != nil {
		return err
	}
	if ok {
		if err := h.recordDowntime(ctx, prev, now, staleClosed); err != nil {
			return err
		}
	}
	return h.st.SetHeartbeat(ctx, h.collector, dbtime.Format(now), false)
}

func (h *heartbeat) recordDowntime(ctx context.Context, prev store.Heartbeat, now time.Time, staleClosed int64) error {
	seen, err := dbtime.Parse(prev.SeenAt)
	if err != nil {
		h.log.Warn("the last heartbeat cannot be read; no gap recorded for the time sentientd was down", "seen_at", prev.SeenAt, "err", err)
		return nil
	}
	if !prev.Clean {
		h.log.Warn("sentientd did not shut down cleanly last time; see the log before this start",
			"last_seen", seen.Local().Format("2006-01-02 15:04:05"))
	}
	switch {
	case seen.After(now):
		h.log.Warn("the last heartbeat is in the future (the clock was set back); no gap recorded", "last_seen", prev.SeenAt)
		return nil
	case staleClosed > 0:
		return nil // the gaps just closed already span the time sentientd was down
	}
	return h.st.RecordClosedGap(ctx, store.Gap{Collector: "sentientd", StartedAt: prev.SeenAt, Reason: health.StoppedReason}, dbtime.Format(now))
}

// beat records that sentientd is running now. A beat the database cannot take
// in time is dropped.
func (h *heartbeat) beat(ctx context.Context) {
	bctx, cancel := context.WithTimeout(ctx, h.wait)
	defer cancel()
	if err := h.st.SetHeartbeat(bctx, h.collector, dbtime.Format(h.now()), false); err != nil && ctx.Err() == nil {
		h.log.Debug("heartbeat not written", "err", err)
	}
}

// run beats until ctx is cancelled.
func (h *heartbeat) run(ctx context.Context) {
	t := time.NewTicker(h.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.beat(ctx)
		}
	}
}

// stop records a stop on purpose, at the exact time. It is called after run has
// returned, so no later beat marks the row not clean again.
func (h *heartbeat) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), h.wait)
	defer cancel()
	if err := h.st.SetHeartbeat(ctx, h.collector, dbtime.Format(h.now()), true); err != nil {
		h.log.Warn("cannot record the clean stop", "err", err)
	}
}

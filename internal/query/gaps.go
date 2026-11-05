package query

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
)

// gapSlack widens a gap on both sides: the agent's log and the kernel's clock
// disagree a little (the merge allows the same), so an action logged just
// after a gap ends is not called unverified because of that. It can hide a real
// unverified action within this long of a gap; that is accepted.
const gapSlack = 5 * time.Second

// GapInfo is a stretch of time in which nobody was recording the kernel (the
// disk was too full, sentientd or Tetragon was down): a monitor_gaps row, for
// the band the prompt page draws around the actions logged in it.
type GapInfo struct {
	From, To time.Time
	Open     bool // still going: To is where the prompt's actions end
	Reason   string
}

// gapSpan is a GapInfo for the arithmetic below.
type gapSpan struct {
	from, to time.Time
	open     bool
	reason   string
}

// info returns the gap as the page shows it.
func (g gapSpan) info() GapInfo {
	return GapInfo{From: g.from, To: g.to, Open: g.open, Reason: g.reason}
}

// gapsAround returns the gaps that touch [from, to]. A gap still open lasts
// through to.
func gapsAround(ctx context.Context, db *sql.DB, from, to time.Time) ([]gapSpan, error) {
	rows, err := db.QueryContext(ctx, `
SELECT started_at, ended_at, reason FROM monitor_gaps
WHERE started_at <= ? AND (ended_at IS NULL OR ended_at >= ?)
ORDER BY started_at`, dbtime.Format(to.Add(gapSlack)), dbtime.Format(from.Add(-gapSlack)))
	if err != nil {
		return nil, fmt.Errorf("gaps: %w", err)
	}
	defer rows.Close()
	var out []gapSpan
	for rows.Next() {
		var start, reason string
		var end sql.NullString
		if err := rows.Scan(&start, &end, &reason); err != nil {
			return nil, err
		}
		s := gapSpan{to: to, open: !end.Valid, reason: reason}
		if s.from, err = dbtime.Parse(start); err != nil {
			return nil, err
		}
		if end.Valid {
			if s.to, err = dbtime.Parse(end.String); err != nil {
				return nil, err
			}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// inGap reports whether t falls in a gap (widened by gapSlack).
func inGap(gaps []gapSpan, t time.Time) bool {
	for _, g := range gaps {
		if !t.Before(g.from.Add(-gapSlack)) && !t.After(g.to.Add(gapSlack)) {
			return true
		}
	}
	return false
}

package web

import (
	"context"
	"time"

	"github.com/nosaka4i/sentient/internal/health"
)

// alertTTL is how long a page may show the last look at Health before the
// next page load asks again (in the background, so no page waits for it).
const alertTTL = 15 * time.Second

// alertWait is how long the very first page waits for Health.
const alertWait = 2 * time.Second

// siteAlert is what every page shows of sentient's health: a banner while
// recording is paused, and a dot on the Health icon while anything needs a look.
type siteAlert struct {
	Paused   bool
	Used     int // percent of the disk used
	ResumeAt int
	Warnings int
	Critical bool
	Label    string // the Health icon's tooltip
	at       time.Time
}

// alertFrom summarizes a health report for the layout; nil when there is
// nothing to say.
func alertFrom(rep *health.Report, now time.Time) *siteAlert {
	if rep == nil {
		return nil
	}
	a := &siteAlert{at: now, Label: "Health: sentient's own status"}
	w := rep.Warnings()
	a.Warnings = len(w)
	for _, x := range w {
		a.Critical = a.Critical || x.Critical
	}
	if s := rep.Storage; s.Paused {
		a.Paused, a.Used, a.ResumeAt = true, s.UsedPct, s.LowPct
		a.Label = "Health: recording paused"
	} else if a.Warnings > 0 {
		a.Label = "Health: " + plural(int64(a.Warnings), "warning")
	}
	return a
}

// notice is the template function behind the banner and the Health icon's dot.
// The first call waits briefly for Health; after that it returns what it last
// saw and refreshes in the background once that is older than alertTTL.
func (s *Server) notice() *siteAlert {
	a := s.siteAlert.Load()
	if a == nil {
		return s.refreshAlert(alertWait)
	}
	if s.now().Sub(a.at) > alertTTL && s.refreshing.CompareAndSwap(false, true) {
		go func() {
			defer s.refreshing.Store(false)
			s.refreshAlert(alertWait)
		}()
	}
	return a
}

// refreshAlert asks Health and remembers the answer. When sentientd cannot
// answer it keeps what it had: the page itself says when it is unreachable.
func (s *Server) refreshAlert(wait time.Duration) *siteAlert {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	rep, err := s.src.Health(ctx)
	if err != nil {
		return s.siteAlert.Load()
	}
	return s.setAlert(rep)
}

// setAlert remembers a health report seen by a handler (the Health page does
// this, so its own banner is never older than the page).
func (s *Server) setAlert(rep *health.Report) *siteAlert {
	a := alertFrom(rep, s.now())
	s.siteAlert.Store(a)
	return a
}

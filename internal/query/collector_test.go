package query

import (
	"context"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/health"
	"github.com/nosaka4i/sentient/internal/store"
)

func TestCollectorStatsOnAnEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var c health.Collector
	if err := CollectorStats(ctx, st.DB, &c); err != nil {
		t.Fatal(err)
	}
	if c.Stored != 0 || !c.FirstEvent.IsZero() || c.Processes != 0 || c.AgentProcesses != 0 || c.Gaps != 0 || c.Latest.Set {
		t.Errorf("empty: %+v", c)
	}
}

func TestCollectorStatsCountsWhatWasStoredAndTheLatestGap(t *testing.T) {
	st, _ := evidenceFixture(t, true) // one kernel event at 09:00:10; six processes, all in an agent's tree
	var c health.Collector
	if err := CollectorStats(context.Background(), st.DB, &c); err != nil {
		t.Fatal(err)
	}
	if c.Stored != 1 || !c.FirstEvent.Equal(time.Date(2026, 10, 17, 9, 0, 10, 0, time.UTC)) || c.Processes != 6 || c.AgentProcesses != 6 {
		t.Errorf("stored: %+v", c)
	}
	if c.Gaps != 0 || c.Latest.Set {
		t.Errorf("no gaps yet: %+v", c)
	}

	addGap(t, st, "09:20:00", "09:25:00")
	addGap(t, st, "09:40:00", "") // still open
	c = health.Collector{}
	if err := CollectorStats(context.Background(), st.DB, &c); err != nil {
		t.Fatal(err)
	}
	l := c.Latest
	if c.Gaps != 2 || !l.Set || !l.Open || !l.To.IsZero() || !l.From.Equal(time.Date(2026, 10, 17, 9, 40, 0, 0, time.UTC)) || l.Reason == "" {
		t.Errorf("gaps = %d, latest %+v", c.Gaps, l)
	}

	// a closed latest gap has its end
	addGap(t, st, "10:00:00", "10:05:00")
	c = health.Collector{}
	if err := CollectorStats(context.Background(), st.DB, &c); err != nil {
		t.Fatal(err)
	}
	if l = c.Latest; l.Open || !l.To.Equal(time.Date(2026, 10, 17, 10, 5, 0, 0, time.UTC)) {
		t.Errorf("closed latest = %+v", l)
	}
}

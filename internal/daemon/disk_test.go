package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/store"
)

// guardAt is a guard with the default low threshold, five under the high one.
func guardAt(t *testing.T, high int) (*diskGuard, *store.Store, *int, *bytes.Buffer) {
	t.Helper()
	return guardBetween(t, high, high-5)
}

// guardBetween is a guard whose disk is whatever the test sets; it logs to buf.
func guardBetween(t *testing.T, high, low int) (*diskGuard, *store.Store, *int, *bytes.Buffer) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	used := 50
	var logs bytes.Buffer
	g := newDiskGuard(st, t.TempDir(), high, low, slog.New(slog.NewTextHandler(&logs, nil)))
	g.statfs = func(string) (int64, int64, int, error) {
		return int64(100-used) * 1_000_000_000, 100_000_000_000, used, nil
	}
	clock := time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC)
	g.now = func() time.Time { clock = clock.Add(10 * time.Minute); return clock }
	return g, st, &used, &logs
}

func gaps(t *testing.T, st *store.Store) [][3]string {
	t.Helper()
	rows, err := st.DB.Query(`SELECT collector, reason, COALESCE(ended_at, '') FROM monitor_gaps ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][3]string
	for rows.Next() {
		var g [3]string
		if err := rows.Scan(&g[0], &g[1], &g[2]); err != nil {
			t.Fatal(err)
		}
		out = append(out, g)
	}
	return out
}

func TestGuardPausesAtTheThresholdAndResumesBelowTheMargin(t *testing.T) {
	ctx := context.Background()
	g, st, used, logs := guardAt(t, 80)

	g.check(ctx)
	if g.Paused() || g.UsedPercent() != 50 || !g.PausedSince().IsZero() {
		t.Fatalf("at 50%%: paused %v, used %d", g.Paused(), g.UsedPercent())
	}
	*used = 79
	g.check(ctx)
	if g.Paused() {
		t.Error("paused at 79%, below the threshold")
	}
	*used = 80
	g.check(ctx)
	if !g.Paused() || g.PausedSince().IsZero() {
		t.Fatal("not paused at 80%")
	}
	got := gaps(t, st)
	if len(got) != 1 || got[0][0] != "sentientd" || got[0][1] != "disk 80% used: recording paused" || got[0][2] != "" {
		t.Errorf("gap while paused = %v", got)
	}
	for _, want := range []string{"level=ERROR", `msg="recording paused: disk 80% used"`, "high=80", "low=75", "free=\"20 GB\"",
		"free space; recording resumes at 75% used"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs.String())
		}
	}

	// still paused while the disk is between the margin and the threshold: no flapping
	for _, u := range []int{78, 76, 79, 80, 90} {
		*used = u
		g.check(ctx)
		if !g.Paused() {
			t.Fatalf("resumed at %d%%, above the resume point", u)
		}
	}
	if n := strings.Count(logs.String(), "recording paused"); n != 1 {
		t.Errorf("logged the pause %d times", n)
	}
	if got := gaps(t, st); len(got) != 1 {
		t.Errorf("a second gap while already paused: %v", got)
	}

	*used = 75
	g.check(ctx)
	if g.Paused() {
		t.Fatal("still paused at 75%")
	}
	got = gaps(t, st)
	if len(got) != 1 || got[0][2] == "" {
		t.Errorf("gap not closed on resume: %v", got)
	}
	if !strings.Contains(logs.String(), "recording resumed") || !strings.Contains(logs.String(), "paused_for=") {
		t.Errorf("no resume line:\n%s", logs.String())
	}
	if !g.PausedSince().IsZero() {
		t.Error("PausedSince set after resume")
	}

	// and it can pause again: a new gap
	*used = 85
	g.check(ctx)
	if got := gaps(t, st); len(got) != 2 || got[1][2] != "" {
		t.Errorf("second pause: %v", got)
	}
}

func TestGuardKeepsRecordingWhenTheDiskCannotBeRead(t *testing.T) {
	g, _, _, _ := guardAt(t, 80)
	g.statfs = func(string) (int64, int64, int, error) { return 0, 0, 0, errors.New("no such device") }
	g.check(context.Background())
	if g.Paused() {
		t.Error("paused on a failed measurement")
	}
	g.statfs = func(string) (int64, int64, int, error) { return 0, 0, 100, nil } // a size of 0 is no reading either
	g.check(context.Background())
	if g.Paused() {
		t.Error("paused on an empty reading")
	}
}

func TestGuardStartedAboveTheThresholdStartsPaused(t *testing.T) {
	g, st, used, _ := guardAt(t, 80)
	*used = 86
	g.check(context.Background()) // the first look, before anything is recorded
	if !g.Paused() || len(gaps(t, st)) != 1 {
		t.Errorf("paused %v, gaps %v", g.Paused(), gaps(t, st))
	}
}

func TestGuardCustomThreshold(t *testing.T) {
	g, _, used, _ := guardAt(t, 60)
	*used = 59
	g.check(context.Background())
	if g.Paused() {
		t.Error("paused below a threshold of 60")
	}
	*used = 60
	g.check(context.Background())
	if !g.Paused() {
		t.Error("not paused at 60")
	}
	*used = 55
	g.check(context.Background())
	if g.Paused() {
		t.Error("still paused at 55, the resume point for 60")
	}
}

func TestGuardRunChecksUntilCancelled(t *testing.T) {
	g, _, _, _ := guardAt(t, 80)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not stop when cancelled")
	}
}

func TestGuardReportsGapWriteFailures(t *testing.T) {
	g, st, used, logs := guardAt(t, 80)
	st.Close() // nothing can be written
	*used = 90
	g.check(context.Background())
	if !g.Paused() { // it still pauses: the writes it protects are the point
		t.Error("not paused when the gap cannot be written")
	}
	if !strings.Contains(logs.String(), "cannot record the pause as a gap") {
		t.Errorf("no error logged:\n%s", logs.String())
	}
	*used = 70
	g.check(context.Background()) // resuming with no gap row to close is fine
	if g.Paused() {
		t.Error("still paused")
	}
}

func TestHealthReportCarriesThePause(t *testing.T) {
	ctx := context.Background()
	g, st, used, _ := guardAt(t, 80)
	m := newMonitor(st, Config{DataDir: t.TempDir(), DiskHigh: 80, DiskLow: 75}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	m.disk = g

	r, err := m.report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.Storage.Paused || r.Storage.HighPct != 80 || r.Storage.LowPct != 75 || !r.Storage.PausedSince.IsZero() || r.Storage.UsedPct < 0 || r.Storage.UsedPct > 100 {
		t.Errorf("not paused: %+v", r.Storage)
	}
	*used = 85
	g.check(ctx)
	if r, err = m.report(ctx); err != nil {
		t.Fatal(err)
	}
	if !r.Storage.Paused || r.Storage.PausedSince.IsZero() {
		t.Errorf("paused: %+v", r.Storage)
	}
	m.disk = nil // no guard (as in tests that do not pause): never paused
	if r, _ = m.report(ctx); r.Storage.Paused {
		t.Error("paused without a guard")
	}
}

func TestUsedPercentIsWhatDfPrints(t *testing.T) {
	for _, c := range []struct {
		used, avail int64
		want        int
	}{
		{0, 100, 0},
		{50, 50, 50},
		{18_688_389_120, 42_580_160_512, 31}, // this machine, df said 31%
		{1, 999, 1},                          // rounds up, like df
		{80, 20, 80},
		{81, 19, 81},
		{0, 0, 0},
	} {
		if got := usedPercent(c.used, c.avail); got != c.want {
			t.Errorf("usedPercent(%d, %d) = %d, want %d", c.used, c.avail, got, c.want)
		}
	}
}

func TestDiskUsageOfARealDirectory(t *testing.T) {
	free, size, used, err := diskUsage(t.TempDir())
	if err != nil || size <= 0 || free < 0 || free > size || used < 0 || used > 100 {
		t.Errorf("diskUsage = %d, %d, %d, %v", free, size, used, err)
	}
	if _, _, _, err := diskUsage("/definitely/not/here"); err == nil {
		t.Error("no error for a missing directory")
	}
}

func TestThresholds(t *testing.T) {
	for _, c := range []struct{ high, low, wantHigh, wantLow int }{
		{0, 0, 80, 75},   // neither set: the defaults
		{80, 0, 80, 75},  // low follows the high one
		{60, 0, 60, 55},  //
		{10, 0, 10, 5},   // the lowest high threshold
		{99, 0, 99, 94},  // the highest
		{80, 60, 80, 60}, // both set
		{80, 79, 80, 79}, // low just under high
		{80, 1, 80, 1},   // and as low as it goes
		{60, 40, 60, 40},
	} {
		high, low, err := thresholds(c.high, c.low)
		if err != nil || high != c.wantHigh || low != c.wantLow {
			t.Errorf("thresholds(%d, %d) = %d, %d, %v; want %d, %d", c.high, c.low, high, low, err, c.wantHigh, c.wantLow)
		}
	}
	for _, c := range []struct {
		high, low int
		want      string
	}{
		{-1, 0, "--disk-high-threshold must be between 10 and 99"},
		{9, 0, "--disk-high-threshold must be between 10 and 99"},
		{100, 0, "--disk-high-threshold must be between 10 and 99"},
		{250, 0, "--disk-high-threshold must be between 10 and 99"},
		{80, 80, "--disk-low-threshold must be between 1 and 79 (below --disk-high-threshold 80)"},
		{80, 95, "--disk-low-threshold must be between 1 and 79"},
		{80, -1, "--disk-low-threshold must be between 1 and 79"},
		{60, 60, "--disk-low-threshold must be between 1 and 59 (below --disk-high-threshold 60)"},
	} {
		if _, _, err := thresholds(c.high, c.low); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("thresholds(%d, %d): err = %v, want %q", c.high, c.low, err, c.want)
		}
	}
}

func TestGuardResumesAtTheLowThresholdYouSet(t *testing.T) {
	ctx := context.Background()
	g, _, used, logs := guardBetween(t, 80, 60)
	*used = 80
	g.check(ctx)
	if !g.Paused() {
		t.Fatal("not paused at 80%")
	}
	for _, u := range []int{75, 70, 61} { // above the low threshold, below the high one: still paused
		*used = u
		g.check(ctx)
		if !g.Paused() {
			t.Fatalf("resumed at %d%%, above the low threshold of 60", u)
		}
	}
	*used = 60
	g.check(ctx)
	if g.Paused() {
		t.Error("still paused at 60%")
	}
	if !strings.Contains(logs.String(), "low=60") || !strings.Contains(logs.String(), "recording resumes at 60% used") {
		t.Errorf("log:\n%s", logs.String())
	}
}

func TestStaleGapsAreClosedWhenSentientdStarts(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	// none open: nothing to say
	if err := closeStaleGaps(ctx, st, time.Now(), log); err != nil || logs.Len() != 0 {
		t.Fatalf("no gaps: %v, %q", err, logs.String())
	}
	tx, _ := st.Begin(ctx)
	tx.OpenGap(ctx, store.Gap{Collector: "sentientd", StartedAt: "2026-11-02T14:00:00.000000000Z", Reason: "disk 81% used: recording paused"})
	tx.OpenGap(ctx, store.Gap{Collector: "tetragon", StartedAt: "2026-11-02T14:05:00.000000000Z", Reason: "stream dropped"})
	id, _ := tx.OpenGap(ctx, store.Gap{Collector: "tetragon", StartedAt: "2026-11-02T13:00:00.000000000Z", Reason: "earlier"})
	tx.CloseGap(ctx, id, "2026-11-02T13:30:00.000000000Z") // already closed: left as it was
	tx.Commit()

	now := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	if err := closeStaleGaps(ctx, st, now, log); err != nil {
		t.Fatal(err)
	}
	got := gaps(t, st)
	if got[0][2] != "2026-11-02T15:00:00.000000000Z" || got[1][2] != "2026-11-02T15:00:00.000000000Z" || got[2][2] != "2026-11-02T13:30:00.000000000Z" {
		t.Errorf("ends = %v", got)
	}
	if !strings.Contains(logs.String(), "closed gaps the last run left open") || !strings.Contains(logs.String(), "count=2") {
		t.Errorf("log:\n%s", logs.String())
	}
	st.Close()
	if err := closeStaleGaps(ctx, st, now, log); err == nil {
		t.Error("closed store: no error")
	}
}

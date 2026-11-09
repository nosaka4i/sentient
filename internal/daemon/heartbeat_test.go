package daemon

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/health"
	"github.com/nosaka4i/sentient/internal/store"
)

const (
	seenAt = "2026-11-09T14:02:11.000000000Z"
	nowAt  = "2026-11-09T14:31:00.000000000Z"
)

// beatAt is a heartbeat on a fresh store whose clock stands at nowAt, with the
// log kept at debug level so every line can be read.
func beatAt(t *testing.T) (*heartbeat, *store.Store, *bytes.Buffer) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var logs bytes.Buffer
	h := newHeartbeat(st, "tetragon", slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	now, _ := time.Parse(time.RFC3339Nano, nowAt)
	h.now = func() time.Time { return now }
	return h, st, &logs
}

func storedBeat(t *testing.T, st *store.Store) store.Heartbeat {
	t.Helper()
	h, ok, err := st.Heartbeat(context.Background(), "tetragon")
	if !ok || err != nil {
		t.Fatalf("heartbeat: ok = %v, err = %v", ok, err)
	}
	return h
}

func TestStartWritesTheDowntimeAsAGap(t *testing.T) {
	ctx := context.Background()
	h, st, logs := beatAt(t)
	st.SetHeartbeat(ctx, "tetragon", seenAt, true) // the last run stopped on purpose

	if err := h.start(ctx, 0); err != nil {
		t.Fatal(err)
	}
	var coll, start, end, reason string
	if err := st.DB.QueryRow(`SELECT collector, started_at, ended_at, reason FROM monitor_gaps`).Scan(&coll, &start, &end, &reason); err != nil {
		t.Fatalf("no gap: %v", err)
	}
	if coll != "sentientd" || start != seenAt || end != nowAt || reason != health.StoppedReason {
		t.Errorf("gap = %q %q %q %q", coll, start, end, reason)
	}
	if b := storedBeat(t, st); b.SeenAt != nowAt || b.Clean {
		t.Errorf("the first beat of this run = %+v", b)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("a clean stop warned:\n%s", logs)
	}
}

func TestStartOnAFirstRunWritesNoGap(t *testing.T) {
	ctx := context.Background()
	h, st, logs := beatAt(t)
	if err := h.start(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if n := gapCount(t, st); n != 0 {
		t.Errorf("%d gaps on a first run", n)
	}
	if b := storedBeat(t, st); b.SeenAt != nowAt || b.Clean {
		t.Errorf("beat = %+v", b)
	}
	if logs.Len() != 0 {
		t.Errorf("log:\n%s", logs)
	}
}

func TestStartAfterACrashWarnsInTheLogAndStillWritesTheGap(t *testing.T) {
	ctx := context.Background()
	h, st, logs := beatAt(t)
	st.SetHeartbeat(ctx, "tetragon", seenAt, false) // never marked clean

	if err := h.start(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if n := gapCount(t, st); n != 1 {
		t.Errorf("%d gaps, want 1", n)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "did not shut down cleanly") ||
		!strings.Contains(logs.String(), "last_seen=") {
		t.Errorf("log:\n%s", logs)
	}
	// the table holds no error text: the gap's reason is the same as for a clean stop
	var reason string
	st.DB.QueryRow(`SELECT reason FROM monitor_gaps`).Scan(&reason)
	if reason != health.StoppedReason {
		t.Errorf("reason = %q", reason)
	}
}

func TestStartWritesNoSecondGapOverAStaleOne(t *testing.T) {
	ctx := context.Background()
	h, st, logs := beatAt(t)
	st.SetHeartbeat(ctx, "tetragon", seenAt, false)

	// closeStaleGaps just ended one: it already spans the downtime
	if err := h.start(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if n := gapCount(t, st); n != 0 {
		t.Errorf("%d gaps written over a stale one", n)
	}
	if !strings.Contains(logs.String(), "did not shut down cleanly") {
		t.Errorf("the crash is still logged:\n%s", logs)
	}
	if b := storedBeat(t, st); b.SeenAt != nowAt {
		t.Errorf("beat = %+v", b)
	}
}

func TestStartIgnoresAHeartbeatInTheFuture(t *testing.T) {
	ctx := context.Background()
	h, st, logs := beatAt(t)
	st.SetHeartbeat(ctx, "tetragon", "2026-11-09T18:00:00.000000000Z", true) // the clock was set back

	if err := h.start(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if n := gapCount(t, st); n != 0 {
		t.Errorf("%d gaps for a heartbeat in the future", n)
	}
	if !strings.Contains(logs.String(), "in the future") {
		t.Errorf("log:\n%s", logs)
	}
	if b := storedBeat(t, st); b.SeenAt != nowAt {
		t.Errorf("the beat was not moved to now: %+v", b)
	}
}

func TestStartSurvivesAnUnreadableHeartbeat(t *testing.T) {
	ctx := context.Background()
	h, st, logs := beatAt(t)
	st.SetHeartbeat(ctx, "tetragon", "not a time", true)

	if err := h.start(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if n := gapCount(t, st); n != 0 {
		t.Errorf("%d gaps", n)
	}
	if !strings.Contains(logs.String(), "cannot be read") {
		t.Errorf("log:\n%s", logs)
	}
	if b := storedBeat(t, st); b.SeenAt != nowAt {
		t.Errorf("beat = %+v", b)
	}
}

func TestStartFailsWithoutADatabase(t *testing.T) {
	h, st, _ := beatAt(t)
	st.Close()
	if err := h.start(context.Background(), 0); err == nil {
		t.Error("closed store: no error")
	}
}

func TestBeatsKeepTheTimeCurrentAndStopMarksItClean(t *testing.T) {
	ctx := context.Background()
	h, st, _ := beatAt(t)
	if err := h.start(ctx, 0); err != nil {
		t.Fatal(err)
	}
	later := time.Date(2026, 11, 9, 14, 31, 30, 0, time.UTC)
	h.now = func() time.Time { return later }
	h.beat(ctx)
	if b := storedBeat(t, st); b.SeenAt != "2026-11-09T14:31:30.000000000Z" || b.Clean {
		t.Errorf("after a beat: %+v", b)
	}
	h.now = func() time.Time { return later.Add(12 * time.Second) }
	h.stop()
	if b := storedBeat(t, st); b.SeenAt != "2026-11-09T14:31:42.000000000Z" || !b.Clean {
		t.Errorf("after a clean stop: %+v", b)
	}
	// the next start sees a clean stop and measures from its exact time
	h.now = func() time.Time { return later.Add(time.Hour) }
	if err := h.start(ctx, 0); err != nil {
		t.Fatal(err)
	}
	var start string
	st.DB.QueryRow(`SELECT started_at FROM monitor_gaps`).Scan(&start)
	if start != "2026-11-09T14:31:42.000000000Z" {
		t.Errorf("the gap starts at %q, want the exact stop", start)
	}
}

func TestABeatThatCannotGetTheDatabaseIsDropped(t *testing.T) {
	ctx := context.Background()
	h, st, logs := beatAt(t)
	if err := h.start(ctx, 0); err != nil {
		t.Fatal(err)
	}
	h.wait = 20 * time.Millisecond
	h.now = func() time.Time { return time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC) }

	tx, err := st.Begin(ctx) // a long write holds the one connection
	if err != nil {
		t.Fatal(err)
	}
	h.beat(ctx)
	tx.Rollback()

	if b := storedBeat(t, st); b.SeenAt != nowAt {
		t.Errorf("the dropped beat changed the row: %+v", b)
	}
	if !strings.Contains(logs.String(), "heartbeat not written") {
		t.Errorf("log:\n%s", logs)
	}
	// the next one goes out on schedule
	h.beat(ctx)
	if b := storedBeat(t, st); b.SeenAt != "2026-11-09T15:00:00.000000000Z" {
		t.Errorf("the next beat: %+v", b)
	}
}

func TestStopWarnsWhenItCannotRecordTheStop(t *testing.T) {
	h, st, logs := beatAt(t)
	st.Close()
	h.stop()
	if !strings.Contains(logs.String(), "cannot record the clean stop") {
		t.Errorf("log:\n%s", logs)
	}
}

func TestRunBeatsOnItsInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h, st, _ := beatAt(t)
	h.every = 10 * time.Millisecond
	n := 0
	h.now = func() time.Time { n++; return time.Date(2026, 11, 9, 14, 0, n, 0, time.UTC) }
	done := make(chan struct{})
	go func() { h.run(ctx); close(done) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, ok, _ := st.Heartbeat(ctx, "tetragon"); ok && b.SeenAt >= "2026-11-09T14:00:03" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no beats written")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

func gapCount(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM monitor_gaps`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRunRecordsTheTimeItWasDownAndMarksACleanStop(t *testing.T) {
	dir, err := os.MkdirTemp("", "sen")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	data := filepath.Join(dir, "data")

	// a previous run, seen long ago and stopped on purpose
	ctx := context.Background()
	prev, err := store.Open(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	prev.SetHeartbeat(ctx, "tetragon", "2026-11-01T10:00:00.000000000Z", true)
	prev.Close()

	ready := make(chan struct{})
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan error)
	go func() {
		done <- Run(rctx, Config{
			DataDir:  data,
			Socket:   filepath.Join(dir, "d.sock"),
			Homes:    []string{filepath.Join(dir, "none")},
			Tetragon: "unix://" + filepath.Join(dir, "no-tetragon.sock"), // nothing listens here
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
			Ready:    ready,
		})
	}()
	<-ready
	// the socket listens before the heartbeat starts: wait for this run's first beat
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err := store.Open(ctx, data)
		if err != nil {
			t.Fatal(err)
		}
		b, _, _ := st.Heartbeat(ctx, "tetragon")
		st.Close()
		if b.SeenAt != "2026-11-01T10:00:00.000000000Z" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run never wrote a heartbeat")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM monitor_gaps WHERE collector = 'sentientd' AND reason = ? AND started_at = '2026-11-01T10:00:00.000000000Z' AND ended_at IS NOT NULL`,
		health.StoppedReason).Scan(&n)
	if n != 1 {
		t.Errorf("%d downtime gaps, want 1", n)
	}
	if b := storedBeat(t, st); !b.Clean || b.SeenAt <= "2026-11-01T10:00:00" {
		t.Errorf("after a clean shutdown: %+v", b)
	}
}

func TestRunWithoutACollectorKeepsNoHeartbeat(t *testing.T) {
	dir, err := os.MkdirTemp("", "sen")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	data := filepath.Join(dir, "data")
	ready := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		done <- Run(ctx, Config{
			DataDir: data,
			Socket:  filepath.Join(dir, "d.sock"),
			Homes:   []string{filepath.Join(dir, "none")},
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Ready:   ready,
		})
	}()
	<-ready
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM heartbeat`).Scan(&n)
	if n != 0 {
		t.Errorf("agent logs only mode wrote %d heartbeat rows", n)
	}
}

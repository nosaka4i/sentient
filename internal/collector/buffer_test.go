package collector

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/store"
)

func openBuffered(t *testing.T, size int) (*BufferedSink, *store.Store) {
	t.Helper()
	s, st := openSink(t)
	_ = s
	return NewBufferedSink(st, "tetragon", size), st
}

var t0 = time.Date(2026, 10, 23, 7, 0, 0, 0, time.UTC)

func feed(t *testing.T, b *BufferedSink, events int) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(b.Process(ctx, store.Process{ExecID: "e1", Collector: "tetragon", PID: 1, Binary: "/bin/sh", StartedAt: dbtime.Format(t0)}))
	for i := 0; i < events; i++ {
		must(b.Event(ctx, store.KernelEvent{ExecID: "e1", TS: dbtime.Format(t0.Add(time.Duration(i) * time.Millisecond)), Type: "file_unlink", Path: fmt.Sprint("/tmp/", i), Payload: `{}`}))
	}
	must(b.Exit(ctx, "e1", t0.Add(time.Second), 3))
}

func count(t *testing.T, st *store.Store, q string) int {
	t.Helper()
	var n int
	if err := st.DB.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBufferedSinkWritesInOrderInBatches(t *testing.T) {
	b, st := openBuffered(t, 5000)
	feed(t, b, 1200) // more than one batch
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for count(t, st, `SELECT COUNT(*) FROM kernel_events`) < 1200 {
		if time.Now().After(deadline) {
			t.Fatal("events were not written")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var code int
	var exited string
	if err := st.DB.QueryRow(`SELECT exit_code, exited_at FROM processes WHERE exec_id = 'e1'`).Scan(&code, &exited); err != nil || code != 3 || exited == "" {
		t.Errorf("process exit = %d %q, %v (exit must apply after the process was added)", code, exited, err)
	}
	if res, err := st.VerifyKernelChainFrom(context.Background(), store.ChainPoint{}); err != nil || res.BadID != 0 || res.Checked != 1200 {
		t.Errorf("chain = %+v, %v", res, err)
	}
	if b.Dropped() != 0 {
		t.Errorf("dropped %d", b.Dropped())
	}
}

func TestBufferedSinkFlushesWhatIsLeftOnShutdown(t *testing.T) {
	b, st := openBuffered(t, 5000)
	feed(t, b, 50)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // stopped before it ever ran
	if err := b.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM kernel_events`); n != 50 {
		t.Errorf("%d events written, want 50", n)
	}
}

func TestBufferedSinkRecordsAGapWhenTheQueueOverflows(t *testing.T) {
	b, st := openBuffered(t, 10)
	feed(t, b, 25) // 27 operations into a queue of 10: 17 are dropped
	if b.Dropped() != 17 {
		t.Fatalf("dropped = %d, want 17", b.Dropped())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var reason string
	var ended *string
	if err := st.DB.QueryRow(`SELECT reason, ended_at FROM monitor_gaps WHERE collector = 'tetragon'`).Scan(&reason, &ended); err != nil {
		t.Fatalf("no gap recorded: %v", err)
	}
	if !strings.Contains(reason, "dropped 17") || ended == nil {
		t.Errorf("gap = %q ended %v", reason, ended)
	}
	if b.Dropped() != 0 {
		t.Errorf("drops not reset: %d", b.Dropped())
	}
}

func TestBufferedSinkGapsAreWrittenAtOnce(t *testing.T) {
	b, st := openBuffered(t, 10)
	ctx := context.Background()
	id, err := b.GapOpened(ctx, t0, "tetragon unreachable")
	if err != nil || id == 0 {
		t.Fatalf("GapOpened = %d, %v", id, err)
	}
	if err := b.GapClosed(ctx, id, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM monitor_gaps WHERE ended_at IS NOT NULL`); n != 1 {
		t.Errorf("%d closed gaps", n)
	}
}

func TestBufferedSinkReturnsStoreErrors(t *testing.T) {
	b, st := openBuffered(t, 10)
	feed(t, b, 2)
	st.DB.Close()
	if err := b.Run(context.Background()); err == nil {
		t.Error("Run succeeded on a closed database")
	}
	// the same on shutdown
	b2, st2 := openBuffered(t, 10)
	feed(t, b2, 2)
	st2.DB.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b2.Run(ctx); err == nil {
		t.Error("flush succeeded on a closed database")
	}
	// and when only the gap cannot be written
	b3, st3 := openBuffered(t, 1)
	feed(t, b3, 3)
	st3.DB.Close()
	if err := b3.recordDrops(context.Background()); err == nil {
		t.Error("recordDrops succeeded on a closed database")
	}
}

package collector

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/store"
)

func openSink(t *testing.T) (*StoreSink, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &StoreSink{Store: st, Collector: "eslogger"}, st
}

// fakeCollector reports a fixed script to the sink, like a real one would.
type fakeCollector struct{}

func (fakeCollector) Name() string { return "fake" }
func (fakeCollector) Run(ctx context.Context, sink Sink) error {
	at := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	if err := sink.Process(ctx, store.Process{ExecID: "e1", Collector: "fake", PID: 1, Binary: "/bin/sh", StartedAt: dbtime.Format(at)}); err != nil {
		return err
	}
	if err := sink.Event(ctx, store.KernelEvent{ExecID: "e1", TS: dbtime.Format(at.Add(time.Second)), Type: "file_unlink", Path: "/tmp/x", Payload: `{}`}); err != nil {
		return err
	}
	id, err := sink.GapOpened(ctx, at.Add(2*time.Second), "stream dropped")
	if err != nil {
		return err
	}
	if err := sink.GapClosed(ctx, id, at.Add(5*time.Second)); err != nil {
		return err
	}
	return sink.Exit(ctx, "e1", at.Add(6*time.Second), 0)
}

func TestStoreSinkWritesWhatACollectorReports(t *testing.T) {
	sink, st := openSink(t)
	var c Collector = fakeCollector{}
	if c.Name() != "fake" {
		t.Fatal("name")
	}
	if err := c.Run(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	var bin, exited string
	if err := st.DB.QueryRow(`SELECT binary, exited_at FROM processes WHERE exec_id = 'e1'`).Scan(&bin, &exited); err != nil || bin != "/bin/sh" || exited != "2026-10-17T09:00:06.000000000Z" {
		t.Errorf("process = %q exited %q, %v", bin, exited, err)
	}
	var typ, path string
	if err := st.DB.QueryRow(`SELECT type, path FROM kernel_events WHERE exec_id = 'e1'`).Scan(&typ, &path); err != nil || typ != "file_unlink" || path != "/tmp/x" {
		t.Errorf("event = %q %q, %v", typ, path, err)
	}
	var coll, started, ended, reason string
	if err := st.DB.QueryRow(`SELECT collector, started_at, ended_at, reason FROM monitor_gaps`).Scan(&coll, &started, &ended, &reason); err != nil ||
		coll != "eslogger" || started != "2026-10-17T09:00:02.000000000Z" || ended != "2026-10-17T09:00:05.000000000Z" || reason != "stream dropped" {
		t.Errorf("gap = %q %q..%q %q, %v", coll, started, ended, reason, err)
	}
	if res, err := st.VerifyKernelChainFrom(context.Background(), store.ChainPoint{}); err != nil || res.BadID != 0 || res.Checked != 1 {
		t.Errorf("chain = %+v, %v", res, err)
	}
}

func TestStoreSinkReturnsStoreErrors(t *testing.T) {
	sink, st := openSink(t)
	st.DB.Close()
	ctx := context.Background()
	now := time.Now()
	if err := sink.Process(ctx, store.Process{ExecID: "e", Binary: "b", StartedAt: "t"}); err == nil {
		t.Error("Process")
	}
	if err := sink.Exit(ctx, "e", now, 0); err == nil {
		t.Error("Exit")
	}
	if err := sink.Event(ctx, store.KernelEvent{TS: "t", Type: "x"}); err == nil {
		t.Error("Event")
	}
	if _, err := sink.GapOpened(ctx, now, "r"); err == nil {
		t.Error("GapOpened")
	}
	if err := sink.GapClosed(ctx, 1, now); err == nil {
		t.Error("GapClosed")
	}
}

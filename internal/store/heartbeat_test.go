package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestHeartbeatIsKeptPerCollectorAndUpdatedInPlace(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if _, ok, err := st.Heartbeat(ctx, "tetragon"); ok || err != nil {
		t.Fatalf("first run: ok = %v, err = %v", ok, err)
	}
	if err := st.SetHeartbeat(ctx, "tetragon", "2026-11-09T10:00:00.000000000Z", false); err != nil {
		t.Fatal(err)
	}
	if err := st.SetHeartbeat(ctx, "eslogger", "2026-11-09T09:00:00.000000000Z", true); err != nil {
		t.Fatal(err)
	}
	h, ok, err := st.Heartbeat(ctx, "tetragon")
	if !ok || err != nil || h.SeenAt != "2026-11-09T10:00:00.000000000Z" || h.Clean {
		t.Fatalf("tetragon = %+v, %v, %v", h, ok, err)
	}

	// an update moves the row, it does not add one
	if err := st.SetHeartbeat(ctx, "tetragon", "2026-11-09T10:00:30.000000000Z", true); err != nil {
		t.Fatal(err)
	}
	h, _, _ = st.Heartbeat(ctx, "tetragon")
	if h.SeenAt != "2026-11-09T10:00:30.000000000Z" || !h.Clean {
		t.Errorf("after the update: %+v", h)
	}
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM heartbeat`).Scan(&n); err != nil || n != 2 {
		t.Errorf("rows = %d, %v; want one per collector", n, err)
	}
	// the other collector's row is untouched
	if o, _, _ := st.Heartbeat(ctx, "eslogger"); o.SeenAt != "2026-11-09T09:00:00.000000000Z" || !o.Clean {
		t.Errorf("eslogger = %+v", o)
	}

	st.Close()
	if _, _, err := st.Heartbeat(ctx, "tetragon"); err == nil {
		t.Error("closed store: Heartbeat has no error")
	}
	if err := st.SetHeartbeat(ctx, "tetragon", "x", false); err == nil {
		t.Error("closed store: SetHeartbeat has no error")
	}
}

func TestRecordClosedGapWritesOneEndedGap(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	g := Gap{Collector: "sentientd", StartedAt: "2026-11-09T10:00:00.000000000Z", Reason: "sentientd was stopped"}
	if err := st.RecordClosedGap(ctx, g, "2026-11-09T10:05:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	var coll, start, end, reason string
	if err := st.DB.QueryRow(`SELECT collector, started_at, ended_at, reason FROM monitor_gaps`).Scan(&coll, &start, &end, &reason); err != nil {
		t.Fatal(err)
	}
	if coll != "sentientd" || start != g.StartedAt || end != "2026-11-09T10:05:00.000000000Z" || reason != g.Reason {
		t.Errorf("gap = %q %q %q %q", coll, start, end, reason)
	}

	st.Close()
	if err := st.RecordClosedGap(ctx, g, "x"); err == nil {
		t.Error("closed store: no error")
	}
}

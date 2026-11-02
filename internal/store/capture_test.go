package store

import (
	"context"
	"testing"
)

func TestRecordCaptureLevelOnAFreshDatabase(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	prev, err := st.RecordCaptureLevel(ctx, "2026-10-30T04:00:00.000000000Z", "essential", "flag")
	if err != nil || prev != "" {
		t.Fatalf("first start: previous %q, %v", prev, err)
	}
	prev, err = st.RecordCaptureLevel(ctx, "2026-10-30T05:00:00.000000000Z", "essential", "flag")
	if err != nil || prev != "essential" {
		t.Fatalf("second start: previous %q, %v", prev, err)
	}
	prev, err = st.RecordCaptureLevel(ctx, "2026-10-30T06:00:00.000000000Z", "complete", "flag")
	if err != nil || prev != "essential" {
		t.Fatalf("change: previous %q, %v", prev, err)
	}
	got, err := st.CaptureLevels(ctx)
	if err != nil || len(got) != 3 {
		t.Fatalf("levels = %v, %v", got, err)
	}
	if got[0].Level != "essential" || got[2].Level != "complete" || got[2].Source != "flag" {
		t.Errorf("levels = %+v", got)
	}
}

func TestRecordCaptureLevelOnADatabaseFromBeforeTheSetting(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	tx, err := st.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AppendKernel(ctx, KernelEvent{TS: "2026-10-29T10:00:00.000000000Z", Type: "exec", ExecID: "e"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	prev, err := st.RecordCaptureLevel(ctx, "2026-10-30T04:00:00.000000000Z", "essential", "flag")
	if err != nil || prev != BeforeSetting {
		t.Fatalf("previous %q, %v; want %q", prev, err, BeforeSetting)
	}
	got, _ := st.CaptureLevels(ctx)
	if len(got) != 2 || got[0].Level != "complete" || got[0].Source != "before-setting" || got[0].TS != "2026-10-29T10:00:00.000000000Z" || got[1].Level != "essential" {
		t.Errorf("levels = %+v", got)
	}
	// the earlier level is recorded once
	if _, err := st.RecordCaptureLevel(ctx, "2026-10-30T05:00:00.000000000Z", "essential", "flag"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.CaptureLevels(ctx); len(got) != 3 {
		t.Errorf("after a second start: %+v", got)
	}
}

func TestCaptureLevelsAreAppendOnly(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	if _, err := st.RecordCaptureLevel(ctx, "t", "essential", "flag"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, `UPDATE capture_levels SET level = 'complete'`); err == nil {
		t.Error("UPDATE succeeded")
	}
	if _, err := st.DB.ExecContext(ctx, `DELETE FROM capture_levels`); err == nil {
		t.Error("DELETE succeeded")
	}
}

func TestCaptureLevelErrors(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	st.Close()
	if _, err := st.RecordCaptureLevel(ctx, "t", "essential", "flag"); err == nil {
		t.Error("RecordCaptureLevel on a closed store")
	}
	if _, err := st.CaptureLevels(ctx); err == nil {
		t.Error("CaptureLevels on a closed store")
	}
}

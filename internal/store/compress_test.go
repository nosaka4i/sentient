package store

import (
	"context"
	"strings"
	"testing"
)

func bigRecord(n int) string {
	return `{"type":"user","text":"` + strings.Repeat("compress me ", n) + `"}`
}

func appendRaws(t *testing.T, st *Store, recs ...string) []int64 {
	t.Helper()
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	var ids []int64
	for i, r := range recs {
		id, err := tx.AppendRaw(ctx, RawRecord{TS: "2026-10-17T09:00:0" + string(rune('0'+i)) + ".000000000Z", SourceFile: "f", Type: "user", Record: r})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func storedType(t *testing.T, st *Store, id int64) string {
	t.Helper()
	var ty string
	if err := st.DB.QueryRow(`SELECT typeof(record) FROM raw_log_records WHERE id = ?`, id).Scan(&ty); err != nil {
		t.Fatal(err)
	}
	return ty
}

func TestLargeRecordsAreStoredCompressedAndRoundTrip(t *testing.T) {
	st := openTest(t)
	big, small := bigRecord(500), `{"a":1}`
	ids := appendRaws(t, st, big, small)
	if got := storedType(t, st, ids[0]); got != "blob" {
		t.Errorf("large record stored as %s", got)
	}
	if got := storedType(t, st, ids[1]); got != "text" {
		t.Errorf("small record stored as %s", got)
	}
	var size int
	st.DB.QueryRow(`SELECT length(record) FROM raw_log_records WHERE id = ?`, ids[0]).Scan(&size)
	if size >= len(big)/10 {
		t.Errorf("compressed to %d of %d bytes", size, len(big))
	}
	for i, want := range []string{big, small} {
		var v any
		st.DB.QueryRow(`SELECT record FROM raw_log_records WHERE id = ?`, ids[i]).Scan(&v)
		if got, err := DecodeRecord(v); err != nil || got != want {
			t.Errorf("record %d did not round trip: %v", i, err)
		}
	}
	// the chain covers the original text
	if bad, err := st.VerifyChain(context.Background()); err != nil || bad != 0 {
		t.Errorf("chain = %d, %v", bad, err)
	}
}

func TestDecodeRecordRejectsBadInput(t *testing.T) {
	if s, err := DecodeRecord(nil); err != nil || s != "" {
		t.Errorf("nil = %q, %v", s, err)
	}
	if _, err := DecodeRecord([]byte("not zstd")); err == nil {
		t.Error("garbage decoded")
	}
	if _, err := DecodeRecord(42); err == nil {
		t.Error("integer decoded")
	}
}

func TestUnreadableRecordBreaksTheChain(t *testing.T) {
	st := openTest(t)
	ids := appendRaws(t, st, bigRecord(300))
	dropGuards(t, st)
	st.DB.Exec(`UPDATE raw_log_records SET record = ? WHERE id = ?`, []byte("garbage"), ids[0])
	if bad, _ := st.VerifyChain(context.Background()); bad != ids[0] {
		t.Errorf("bad = %d, want %d", bad, ids[0])
	}
}

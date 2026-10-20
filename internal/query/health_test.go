package query

import (
	"context"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/store"
)

func TestHealthQueries(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Empty store: adapters still listed, nothing else.
	ing, err := IngestStats(ctx, st.DB)
	if err != nil || len(ing) != 1 || ing[0].Agent != "claude" || !ing[0].LastRecord.IsZero() {
		t.Fatalf("empty ingest = %+v, %v", ing, err)
	}
	if b, err := HistoryBegins(ctx, st.DB); err != nil || !b.IsZero() {
		t.Errorf("empty history = %v, %v", b, err)
	}

	ts := func(s string) string { return "2026-10-17T" + s + ".000000000Z" }
	tx, _ := st.Begin(ctx)
	s1, _ := tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "aaaa1111", Cwd: "/w/proj", RepoRoot: "/w/proj", TS: ts("09:00:00"), AgentVersion: "2.0.1"})
	tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "aaaa1111", TS: ts("11:58:00"), AgentVersion: "2.0.9"})
	tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "bbbb2222", TS: ts("08:00:00"), AgentVersion: "1.9.0"})
	tx.UpsertSession(ctx, store.Session{Agent: "codex", AgentSessionID: "cccc3333", Cwd: "/w/x", TS: ts("11:59:00")})
	tx.AppendRaw(ctx, store.RawRecord{SessionID: s1, TS: ts("09:00:00"), SourceFile: "f", Type: "user", Record: "{}"})
	tx.AppendRaw(ctx, store.RawRecord{SessionID: s1, SourceFile: "f", Type: "mystery", Unknown: true, Record: "{}"})
	tx.AppendRaw(ctx, store.RawRecord{SourceFile: "f", Unknown: true, Record: "{bad"})
	tx.SetOffset(ctx, "/h/.claude/projects/p/a.jsonl", 10)
	tx.SetOffset(ctx, "/h/.claude/projects/p/b.jsonl", 10)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	ing, err = IngestStats(ctx, st.DB)
	if err != nil || len(ing) != 2 {
		t.Fatalf("ingest = %+v, %v", ing, err)
	}
	c := ing[0]
	if c.Agent != "claude" || c.Files != 2 || c.Unknown != 2 || !c.LastRecord.Equal(time.Date(2026, 10, 17, 11, 58, 0, 0, time.UTC)) {
		t.Errorf("claude ingest = %+v", c)
	}
	if ing[1].Agent != "codex" || ing[1].Files != 0 {
		t.Errorf("codex ingest = %+v", ing[1])
	}

	ags, err := Agents(ctx, st.DB)
	if err != nil || len(ags) != 1 || ags[0].Version != "2.0.9" || ags[0].Logs != "~/.claude/projects" {
		t.Errorf("agents = %+v, %v", ags, err)
	}

	live, err := LiveSessions(ctx, st.DB, time.Date(2026, 10, 17, 11, 55, 0, 0, time.UTC))
	if err != nil || len(live) != 2 || live[0].SessionID != "cccc3333" || live[0].Repo != "x" || live[1].Repo != "proj" {
		t.Errorf("live = %+v, %v", live, err)
	}

	st.Close()
	if _, err := IngestStats(ctx, st.DB); err == nil {
		t.Error("want error on closed db")
	}
	if _, err := Agents(ctx, st.DB); err == nil {
		t.Error("want error on closed db")
	}
	if _, err := LiveSessions(ctx, st.DB, time.Now()); err == nil {
		t.Error("want error on closed db")
	}
	if _, err := HistoryBegins(ctx, st.DB); err == nil {
		t.Error("want error on closed db")
	}
}

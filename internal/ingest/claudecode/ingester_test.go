package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/store"
)

const fixtureSession = "11111111-1111-4111-8111-111111111111"

// fixtureHome copies testdata/claudecode/home into a temp dir so tests
// can append to and truncate transcripts.
func fixtureHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.CopyFS(home, os.DirFS("../../../testdata/claudecode/home")); err != nil {
		t.Fatal(err)
	}
	return home
}

func mainFile(home string) string {
	return filepath.Join(home, ".claude", "projects", "-home-dev-proj", fixtureSession+".jsonl")
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func count(t *testing.T, st *store.Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := st.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestIngestFixture(t *testing.T) {
	ctx := context.Background()
	home := fixtureHome(t)
	st := openStore(t)
	in := New(st, Config{Homes: []string{home}})

	if got := len(in.Files()); got != 2 {
		t.Fatalf("Files() = %d, want 2", got)
	}
	if err := in.ScanOnce(ctx); err != nil {
		t.Fatal(err)
	}

	if n := count(t, st, `SELECT COUNT(*) FROM raw_log_records`); n != 25 {
		t.Errorf("raw records = %d, want 25 (every line stored)", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM raw_log_records WHERE unknown = 1`); n != 2 {
		t.Errorf("unknown raw records = %d, want 2", n)
	}
	unk := in.UnknownCounts()
	if unk["brand-new-type"] != 1 || unk["<unparsable>"] != 1 {
		t.Errorf("UnknownCounts = %v", unk)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM sessions`); n != 1 {
		t.Errorf("sessions = %d, want 1 (subagent folds into parent)", n)
	}
	var cwd, started, ended, ver string
	if err := st.DB.QueryRow(`SELECT cwd, started_at, ended_at, agent_version FROM sessions`).Scan(&cwd, &started, &ended, &ver); err != nil {
		t.Fatal(err)
	}
	if ver != "2.0.5" {
		t.Errorf("agent_version = %q, want the newest seen (2.0.5)", ver)
	}
	if cwd != "/home/dev/proj" || started != "2026-10-17T09:00:00.000000000Z" || ended != "2026-10-17T09:31:00.000000000Z" {
		t.Errorf("session = %q %q %q", cwd, started, ended)
	}

	// Prompts: only the two typed messages.
	rows, err := st.DB.Query(`SELECT seq, text, COALESCE(ended_at, '') FROM prompts ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	var prompts []string
	for rows.Next() {
		var seq int
		var text, end string
		if err := rows.Scan(&seq, &text, &end); err != nil {
			t.Fatal(err)
		}
		prompts = append(prompts, text)
		if seq == 1 && end != "2026-10-17T09:30:00.000000000Z" {
			t.Errorf("prompt 1 ended_at = %q, want start of prompt 2", end)
		}
	}
	rows.Close()
	want := []string{"list the files and fix the typo", "search the web for go tabwriter"}
	if len(prompts) != 2 || prompts[0] != want[0] || prompts[1] != want[1] {
		t.Errorf("prompts = %q, want %q", prompts, want)
	}

	// Actions.
	type act struct {
		res, verb, path string
		failed, add, rm int
		seq             int
	}
	got := map[string]act{}
	rows, err = st.DB.Query(`
SELECT a.tool_use_id, a.resource, a.action, COALESCE(a.path, ''), a.failed, a.lines_added, a.lines_removed, COALESCE(p.seq, 0), a.evidence
FROM actions a LEFT JOIN prompts p ON p.id = a.prompt_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, ev string
		var a act
		if err := rows.Scan(&id, &a.res, &a.verb, &a.path, &a.failed, &a.add, &a.rm, &a.seq, &ev); err != nil {
			t.Fatal(err)
		}
		if ev != "log" {
			t.Errorf("%s evidence = %q", id, ev)
		}
		got[id] = a
	}
	rows.Close()
	wantActs := map[string]act{
		"tu1":  {"command", "execute", "ls -la", 0, 0, 0, 1},
		"tu2":  {"file", "read", "/home/dev/proj/README.md", 0, 0, 0, 1},
		"tu3":  {"file", "update", "/home/dev/proj/README.md", 0, 1, 1, 1},
		"tu4":  {"file", "create", "/home/dev/proj/NOTES.md", 0, 2, 0, 1},
		"tu5":  {"command", "execute", "false", 1, 0, 0, 1},
		"tu6":  {"web", "search", "go tabwriter", 0, 0, 0, 2},
		"tu7":  {"web", "fetch", "https://pkg.go.dev/text/tabwriter", 0, 0, 0, 2},
		"tu8":  {"subagent", "start", "Explore", 0, 0, 0, 2},
		"tu10": {"file", "read", "/etc/hosts", 0, 0, 0, 2},
	}
	if len(got) != len(wantActs) {
		t.Errorf("got %d actions, want %d: %v", len(got), len(wantActs), got)
	}
	for id, w := range wantActs {
		if got[id] != w {
			t.Errorf("action %s = %+v, want %+v", id, got[id], w)
		}
	}

	if bad, err := st.VerifyChain(ctx); err != nil || bad != 0 {
		t.Errorf("VerifyChain = %d, %v", bad, err)
	}
}

func TestResumeAndPartialLines(t *testing.T) {
	ctx := context.Background()
	home := fixtureHome(t)
	st := openStore(t)
	in := New(st, Config{Homes: []string{home}})
	for i := 0; i < 2; i++ {
		if err := in.ScanOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, st, `SELECT COUNT(*) FROM raw_log_records`); n != 25 {
		t.Fatalf("after rescan raw = %d, want 25 (no re-ingest)", n)
	}

	// A restarted ingester resumes from the stored offset.
	in2 := New(st, Config{Homes: []string{home}})
	f, err := os.OpenFile(mainFile(home), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	partial := `{"type":"user","sessionId":"` + fixtureSession + `","timestamp":"2026-10-17T10:00:00Z","origin":{"kind":"human"},"message":{"role":"user","content":"third prompt"}}`
	if _, err := f.WriteString(partial[:40]); err != nil {
		t.Fatal(err)
	}
	if err := in2.ScanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM raw_log_records`); n != 25 {
		t.Fatalf("partial line ingested early: raw = %d", n)
	}
	if _, err := f.WriteString(partial[40:] + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := in2.ScanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM raw_log_records`); n != 26 {
		t.Fatalf("raw = %d, want 26", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM prompts WHERE text = 'third prompt' AND seq = 3`); n != 1 {
		t.Fatalf("third prompt not ingested as seq 3")
	}
}

func TestShrunkFileIsReread(t *testing.T) {
	ctx := context.Background()
	home := fixtureHome(t)
	st := openStore(t)
	in := New(st, Config{Homes: []string{home}})
	if err := in.ScanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"mode","mode":"normal","sessionId":"` + fixtureSession + `"}` + "\n"
	if err := os.WriteFile(mainFile(home), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := in.ScanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM raw_log_records WHERE type = 'mode'`); n != 1 {
		t.Fatalf("mode records = %d, want 1", n)
	}
}

func TestScanErrorsAreReported(t *testing.T) {
	st := openStore(t)
	in := New(st, Config{Homes: []string{t.TempDir()}})
	if err := in.IngestFile(context.Background(), "/nonexistent/x.jsonl"); err == nil {
		t.Fatal("want error for missing file")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := in.ScanOnce(ctx); err != nil {
		t.Fatalf("ScanOnce with no files: %v", err)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	home := fixtureHome(t)
	st := openStore(t)
	in := New(st, Config{Homes: []string{home}, PollInterval: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- in.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for count(t, st, `SELECT COUNT(*) FROM raw_log_records`) < 25 {
		if time.Now().After(deadline) {
			t.Fatal("Run did not ingest")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestBashChainBecomesOneActionPerCommand(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "-p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := `{"type":"user","sessionId":"s","timestamp":"2026-10-17T09:00:00Z","origin":{"kind":"human"},"message":{"role":"user","content":"test it"}}
{"type":"assistant","sessionId":"s","timestamp":"2026-10-17T09:00:01Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"cd /w && go test ./... | tail -1"}}]}}
{"type":"user","sessionId":"s","timestamp":"2026-10-17T09:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":true}]}}
`
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	st := openStore(t)
	if err := New(st, Config{Homes: []string{home}}).ScanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := st.DB.Query(`SELECT part, path, failed, prompt_id IS NOT NULL FROM actions ORDER BY part`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var part, failed int
		var path string
		var hasPrompt bool
		if err := rows.Scan(&part, &path, &failed, &hasPrompt); err != nil {
			t.Fatal(err)
		}
		if !hasPrompt {
			t.Errorf("part %d not linked to the prompt", part)
		}
		got = append(got, path+map[int]string{0: "", 1: " (failed)"}[failed])
	}
	want := []string{"cd /w", "go test ./...", "tail -1 (failed)"} // failure marks the last part only
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("actions = %q, want %q", got, want)
	}
}

func TestRepoRoot(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FindRepoRoot(sub); got != repo {
		t.Errorf("FindRepoRoot = %q, want %q", got, repo)
	}
	if got := FindRepoRoot(dir); got != "" {
		t.Errorf("FindRepoRoot outside repo = %q", got)
	}
	in := New(openStore(t), Config{})
	if in.repoRoot(sub) != repo || in.repoRoot(sub) != repo || in.repoRoot("") != "" {
		t.Error("repoRoot cache")
	}
}

func TestSessionFromPath(t *testing.T) {
	for path, want := range map[string]string{
		"/h/.claude/projects/p/abc.jsonl":                   "abc",
		"/h/.claude/projects/p/abc/subagents/agent-x.jsonl": "abc",
	} {
		if got := sessionFromPath(path); got != want {
			t.Errorf("sessionFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}

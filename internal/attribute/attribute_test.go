package attribute

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/store"
)

var t0 = time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)

func ts(sec int) string { return dbtime.Format(t0.Add(time.Duration(sec) * time.Second)) }

const claude = "/home/u/.local/share/claude/versions/1"

func isAgent(b string) bool { return b == claude }

type fixture struct {
	t   *testing.T
	st  *store.Store
	ctx context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &fixture{t: t, st: st, ctx: context.Background()}
}

// session adds a session in cwd with log activity at the given seconds and a
// prompt at each.
func (f *fixture) session(id, cwd string, active ...int) int64 {
	f.t.Helper()
	tx, err := f.st.Begin(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	defer tx.Rollback()
	sid, err := tx.UpsertSession(f.ctx, store.Session{Agent: "claude", AgentSessionID: id, Cwd: cwd, TS: ts(active[0])})
	if err != nil {
		f.t.Fatal(err)
	}
	for _, s := range active {
		if _, err := tx.AppendRaw(f.ctx, store.RawRecord{SessionID: sid, TS: ts(s), SourceFile: "f", Type: "user", Record: "{}"}); err != nil {
			f.t.Fatal(err)
		}
		if _, err := tx.AddPrompt(f.ctx, sid, "prompt at "+ts(s), ts(s)); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		f.t.Fatal(err)
	}
	return sid
}

func (f *fixture) proc(execID, parent, binary, cwd string, start int) {
	f.t.Helper()
	tx, err := f.st.Begin(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	defer tx.Rollback()
	if err := tx.AddProcess(f.ctx, store.Process{ExecID: execID, Collector: "tetragon", PID: 1, ParentExecID: parent, Binary: binary, Cwd: cwd, StartedAt: ts(start)}); err != nil {
		f.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) get(execID string) (session, prompt int64, how string) {
	f.t.Helper()
	var s, p *int64
	var h *string
	if err := f.st.DB.QueryRow(`SELECT session_id, prompt_id, attribution FROM processes WHERE exec_id = ?`, execID).Scan(&s, &p, &h); err != nil {
		f.t.Fatal(err)
	}
	if s != nil {
		session = *s
	}
	if p != nil {
		prompt = *p
	}
	if h != nil {
		how = *h
	}
	return
}

func (f *fixture) att() *Attributor {
	return &Attributor{St: f.st, IsAgent: isAgent, Now: func() time.Time { return t0.Add(24 * time.Hour) }}
}

func TestRootAndDescendantsGoToTheSessionInTheirDirectory(t *testing.T) {
	f := newFixture(t)
	a := f.session("a", "/work/a", 2, 30)
	f.session("b", "/work/b", 2, 30)
	f.proc("root", "", claude, "/work/a", 0)
	f.proc("sh", "root", "/bin/sh", "/work/a", 10)
	f.proc("rm", "sh", "/usr/bin/rm", "/work/a", 35)

	n, err := f.att().Once(f.ctx)
	if err != nil || n != 3 {
		t.Fatalf("Once = %d, %v", n, err)
	}
	if s, p, how := f.get("root"); s != a || p != 0 || how != Exact {
		t.Errorf("root = %d %d %q", s, p, how)
	}
	// started between prompts at 2 and 30: the first prompt is in effect
	if s, p, how := f.get("sh"); s != a || p == 0 || how != Exact {
		t.Errorf("sh = %d %d %q", s, p, how)
	}
	_, shPrompt, _ := f.get("sh")
	_, rmPrompt, _ := f.get("rm")
	if shPrompt == rmPrompt {
		t.Errorf("rm at 35 should be in the later prompt than sh at 10, both %d", shPrompt)
	}
}

func TestSeveralSessionsInOneDirectoryAreAmbiguous(t *testing.T) {
	f := newFixture(t)
	first := f.session("a", "/work", 1, 20)
	f.session("b", "/work", 5, 25)
	f.proc("root", "", claude, "/work", 0)
	f.proc("child", "root", "/bin/sh", "/work", 12)
	if _, err := f.att().Once(f.ctx); err != nil {
		t.Fatal(err)
	}
	if s, _, how := f.get("root"); s != first || how != Ambiguous {
		t.Errorf("root = %d %q, want session %d ambiguous", s, how, first)
	}
	if _, _, how := f.get("child"); how != Ambiguous {
		t.Errorf("child inherits the doubt, got %q", how)
	}
}

func TestRootWithNoLogYetIsTriedAgainLater(t *testing.T) {
	f := newFixture(t)
	f.proc("root", "", claude, "/work", 0)
	f.proc("child", "root", "/bin/sh", "/work", 3)
	if n, err := f.att().Once(f.ctx); err != nil || n != 0 {
		t.Fatalf("first pass = %d, %v", n, err)
	}
	sid := f.session("a", "/work", 2)
	if n, err := f.att().Once(f.ctx); err != nil || n != 2 {
		t.Fatalf("second pass = %d, %v", n, err)
	}
	if s, _, _ := f.get("child"); s != sid {
		t.Errorf("child = %d, want %d", s, sid)
	}
}

func TestOnlyAgentRootsStartASession(t *testing.T) {
	f := newFixture(t)
	f.session("a", "/work", 1)
	f.proc("stray", "", "/usr/bin/ps", "/work", 0) // not an agent, parent unknown
	f.proc("root", "", claude, "/nowhere", 0)      // no session there
	f.proc("helper", "root", claude, "/work", 1)   // an agent's helper: its parent decides
	if n, err := f.att().Once(f.ctx); err != nil || n != 0 {
		t.Fatalf("Once = %d, %v", n, err)
	}
	if s, _, _ := f.get("stray"); s != 0 {
		t.Error("a process outside any agent was attributed")
	}
}

func TestARootOnlyMatchesActivityAfterItStarted(t *testing.T) {
	f := newFixture(t)
	f.session("early", "/work", 1)
	late := f.session("late", "/work", 5000)
	f.proc("root", "", claude, "/work", 4990)
	if _, err := f.att().Once(f.ctx); err != nil {
		t.Fatal(err)
	}
	if s, _, how := f.get("root"); s != late || how != Exact {
		t.Errorf("root = %d %q, want session %d exact", s, how, late)
	}
}

func TestRunStopsWithContext(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	a := f.att()
	go func() { done <- a.Run(ctx, 5*time.Millisecond) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOnceReportsStoreErrors(t *testing.T) {
	f := newFixture(t)
	f.st.DB.Close()
	if _, err := f.att().Once(f.ctx); err == nil || strings.Contains(err.Error(), "nil") {
		t.Errorf("err = %v", err)
	}
}

func TestASessionBornWithTheProcessBeatsOneItMerelyResumed(t *testing.T) {
	f := newFixture(t)
	f.session("old", "/work", -1000, 10) // started long before, still being used
	fresh := f.session("fresh", "/work", 2, 20)
	f.session("later", "/work", 500) // began after the process: someone else's
	f.proc("root", "", claude, "/work", 0)
	if _, err := f.att().Once(f.ctx); err != nil {
		t.Fatal(err)
	}
	if s, _, how := f.get("root"); s != fresh || how != Exact {
		t.Errorf("root = %d %q, want session %d exact", s, how, fresh)
	}
}

func TestAResumedSessionIsTheOnlyChoiceWhenNothingWasBornWithTheProcess(t *testing.T) {
	f := newFixture(t)
	old := f.session("old", "/work", -1000, 10)
	f.proc("root", "", claude, "/work", 0)
	if _, err := f.att().Once(f.ctx); err != nil {
		t.Fatal(err)
	}
	if s, _, how := f.get("root"); s != old || how != Exact {
		t.Errorf("root = %d %q, want session %d exact", s, how, old)
	}
}

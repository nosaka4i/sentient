package query

import (
	"context"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/store"
)

// evidenceFixture: one prompt (09:00-09:10) with logged commands (one confirmed,
// one the kernel missed, one builtin), an unreported kernel command, and an
// agent process whose background commands ran during the prompt and after it.
// watched adds a kernel event during the prompt.
func evidenceFixture(t *testing.T, watched bool) (*store.Store, int64) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := func(s string) string { return "2026-10-17T" + s + ".000000000Z" }

	tx, _ := st.Begin(ctx)
	sid, _ := tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "s", Cwd: "/w", TS: ts("09:00:00")})
	pid, _ := tx.AddPrompt(ctx, sid, "do it", ts("09:00:00"))
	logged := func(at, path, ev string, tool string) {
		if err := tx.AddAction(ctx, store.Action{SessionID: sid, PromptID: pid, TS: ts(at), Resource: "command", Action: "execute",
			Path: path, Evidence: ev, ToolUseID: tool}); err != nil {
			t.Fatal(err)
		}
	}
	logged("09:01:00", "make", "both", "t1")
	logged("09:02:00", "curl x", "log", "t2")
	logged("09:03:00", "cd /w", "log", "t3")
	if err := tx.AddAction(ctx, store.Action{SessionID: sid, PromptID: pid, TS: ts("09:04:00"), Resource: "file", Action: "read", Path: "/w/a", Evidence: "log", ToolUseID: "t4"}); err != nil {
		t.Fatal(err)
	}
	// a web fetch is done on this machine but cannot be compared with the kernel yet; a
	// web search and a subagent start are not done on this machine at all
	for i, a := range []store.Action{
		{Resource: "web", Action: "fetch", Path: "https://example.com"},
		{Resource: "web", Action: "search", Path: "a query"},
		{Resource: "subagent", Action: "start", Path: "general"},
	} {
		a.SessionID, a.PromptID, a.TS, a.Evidence, a.ToolUseID = sid, pid, ts("09:04:30"), "log", "w"+string(rune('1'+i))
		if err := tx.AddAction(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	proc := func(exec, parent string, at string, bin, argv string) {
		if err := tx.AddProcess(ctx, store.Process{ExecID: exec, Collector: "tetragon", PID: int64(len(exec)) + 100, ParentExecID: parent, Binary: bin, Argv: argv, StartedAt: ts(at)}); err != nil {
			t.Fatal(err)
		}
		if err := tx.AttributeProcess(ctx, exec, sid, pid); err != nil {
			t.Fatal(err)
		}
	}
	proc("root", "", "08:59:00", "/opt/claude/versions/2.1.9", `["-c"]`)
	proc("b1", "root", "09:00:10", "/usr/bin/git", `["status"]`)
	proc("b2", "root", "09:00:20", "/opt/claude/versions/2.1.9", `["-c"]`)
	proc("b3", "root", "09:05:00", "/usr/bin/git", `["status"]`)
	proc("late", "root", "09:30:00", "/usr/bin/git", `["status"]`) // after the prompt
	for exec, key := range map[string]string{"b1": "git status", "b2": "claude -c", "b3": "git status", "late": "git status"} {
		if err := tx.SetBackground(ctx, exec, key, 1); err != nil {
			t.Fatal(err)
		}
	}
	proc("u", "root", "09:06:00", "/usr/bin/id", `[]`)
	if err := tx.AddKernelAction(ctx, store.Action{SessionID: sid, PromptID: pid, ExecID: "u", TS: ts("09:06:00"), Resource: "command", Action: "execute", Path: "id"}); err != nil {
		t.Fatal(err)
	}
	if watched {
		if _, err := tx.AppendKernel(ctx, store.KernelEvent{ExecID: "root", TS: ts("09:00:10"), Type: "exec", Payload: "{}"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	st.DB.Exec(`UPDATE prompts SET ended_at = ?, last_activity_at = ? WHERE id = ?`, ts("09:10:00"), ts("09:10:00"), pid)
	return st, pid
}

var evidenceNow = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

// assertAccounting checks the two sums the page shows, which must hold for any
// prompt: every logged action is exactly one of confirmed, unverified, not
// checked or pending, and everything the kernel saw is confirmed or unreported.
func assertAccounting(t *testing.T, c EvidenceCounts) {
	t.Helper()
	if got := c.Confirmed + c.Unverified + c.NotChecked + c.NotCompared + c.Pending; got != c.AgentLog {
		t.Errorf("agent log %d != confirmed %d + unverified %d + not checked %d + not compared %d + pending %d = %d",
			c.AgentLog, c.Confirmed, c.Unverified, c.NotChecked, c.NotCompared, c.Pending, got)
	}
	if got := c.Confirmed + c.Unreported; got != c.KernelLog {
		t.Errorf("kernel log %d != confirmed %d + unreported %d = %d", c.KernelLog, c.Confirmed, c.Unreported, got)
	}
}

func TestPromptEvidenceCounts(t *testing.T) {
	ctx := context.Background()
	st, pid := evidenceFixture(t, true)
	d, err := GetPromptDetail(ctx, st.DB, pid, evidenceNow)
	if err != nil {
		t.Fatal(err)
	}
	// agent log: make, curl, the file read and the web fetch (the builtin cd is
	// left out, and so are the web search and the subagent start, which are not
	// done on this machine). The kernel saw make and the unreported id. curl and
	// the read have no kernel record (unverified); the fetch cannot be compared
	// yet (not checked). Three background commands ran while the prompt did; the
	// one at 09:30 came after it.
	want := EvidenceCounts{Watched: true, AgentLog: 4, KernelLog: 2, Confirmed: 1, Unverified: 2, NotChecked: 1, Unreported: 1, Background: 3, AgentID: "root"}
	if d.Counts != want {
		t.Errorf("counts = %+v, want %+v", d.Counts, want)
	}
	assertAccounting(t, d.Counts)

	var unreported, unverified int
	var last int64
	for _, s := range d.Steps {
		last = s.N
		if s.Unreported {
			unreported++
		}
		if s.Unverified {
			unverified++
		}
	}
	if unreported != 1 || unverified != 2 {
		t.Errorf("unreported %d, unverified %d", unreported, unverified)
	}
	// the prompt, then make curl cd read the web actions and id: background commands are no steps
	if last != 9 || len(d.Steps) != 9 {
		t.Errorf("steps = %d, last number %d, want 6", len(d.Steps), last)
	}
	// kernel-only actions are not part of the activity tiles
	for _, r := range d.Activity {
		if r.Resource == "command" && r.Count != 3 {
			t.Errorf("command tile = %d, want 3 (the logged commands)", r.Count)
		}
	}
}

func TestPromptEvidenceWithoutKernel(t *testing.T) {
	ctx := context.Background()
	st, pid := evidenceFixture(t, false)
	d, err := GetPromptDetail(ctx, st.DB, pid, evidenceNow)
	if err != nil {
		t.Fatal(err)
	}
	// nothing was recording: a logged command the kernel did not see is not
	// unverified, and no background commands are counted
	if d.Counts.Watched || d.Counts.Unverified != 0 || d.Counts.Background != 0 || d.Counts.AgentID != "" {
		t.Errorf("counts = %+v", d.Counts)
	}
	assertAccounting(t, d.Counts)
}

func addGap(t *testing.T, st *store.Store, start, end string) {
	t.Helper()
	var endAt any
	if end != "" {
		endAt = "2026-10-17T" + end + ".000000000Z"
	}
	if _, err := st.DB.Exec(`INSERT INTO monitor_gaps (collector, started_at, ended_at, reason) VALUES ('sentientd', ?, ?, 'disk 81% used: recording paused')`,
		"2026-10-17T"+start+".000000000Z", endAt); err != nil {
		t.Fatal(err)
	}
}

func counted(t *testing.T, st *store.Store, pid int64) *PromptDetail {
	t.Helper()
	d, err := GetPromptDetail(context.Background(), st.DB, pid, evidenceNow)
	if err != nil {
		t.Fatal(err)
	}
	assertAccounting(t, d.Counts)
	return d
}

// The kernel has no record of what was logged while nobody was recording: that is
// "not compared", neither confirmed nor unverified. What the kernel did match stays
// confirmed, and what falls outside every gap is still a finding.
func TestActionsInAGapAreNotCompared(t *testing.T) {
	st, pid := evidenceFixture(t, true)
	addGap(t, st, "09:00:50", "09:02:10") // covers make (confirmed) and curl (logged only)
	d := counted(t, st, pid)
	want := EvidenceCounts{Watched: true, AgentLog: 4, KernelLog: 2, Confirmed: 1, Unverified: 1, NotChecked: 1, NotCompared: 1, Unreported: 1, Background: 3, AgentID: "root"}
	if d.Counts != want {
		t.Errorf("counts = %+v, want %+v", d.Counts, want)
	}
	byPath := map[string]Step{}
	for _, s := range d.Steps {
		byPath[s.Path] = s
	}
	if s := byPath["curl x"]; !s.NotCompared || s.Unverified {
		t.Errorf("curl in the gap: %+v", s)
	}
	if s := byPath["make"]; s.NotCompared || s.Unverified || s.Evidence != "both" {
		t.Errorf("make was matched by the kernel, so it stays confirmed: %+v", s)
	}
	if s := byPath["/w/a"]; s.NotCompared || !s.Unverified {
		t.Errorf("the read at 09:04 is outside the gap: %+v", s)
	}
	// the gap itself comes with the detail, for the band's label
	if len(d.Gaps) != 1 || d.Gaps[0].Reason != "disk 81% used: recording paused" || d.Gaps[0].Open ||
		d.Gaps[0].To.Sub(d.Gaps[0].From) != 80*time.Second {
		t.Errorf("gaps = %+v", d.Gaps)
	}
}

func TestGapEdgesAreWidenedByFiveSeconds(t *testing.T) {
	// the file read is logged at 09:04:00
	for _, c := range []struct {
		end         string
		notCompared bool
		describe    string
	}{
		{"09:03:57", true, "3 seconds after the gap"},
		{"09:03:55", true, "5 seconds after the gap"},
		{"09:03:54", false, "6 seconds after the gap"},
	} {
		st, pid := evidenceFixture(t, true)
		addGap(t, st, "09:03:00", c.end)
		d := counted(t, st, pid)
		var read Step
		for _, s := range d.Steps {
			if s.Path == "/w/a" {
				read = s
			}
		}
		if read.NotCompared != c.notCompared || read.Unverified == c.notCompared {
			t.Errorf("read %s: %+v", c.describe, read)
		}
	}
	// and before it
	st, pid := evidenceFixture(t, true)
	addGap(t, st, "09:04:04", "09:05:00")
	d := counted(t, st, pid)
	if d.Counts.NotCompared != 1 {
		t.Errorf("4 seconds before the gap: %+v", d.Counts)
	}
}

func TestAnOpenGapLastsThroughThePrompt(t *testing.T) {
	st, pid := evidenceFixture(t, true)
	addGap(t, st, "09:01:30", "") // still open: Tetragon is down
	d := counted(t, st, pid)
	if d.Counts.NotCompared != 2 || d.Counts.Unverified != 0 { // curl and the read
		t.Errorf("counts = %+v", d.Counts)
	}
	if len(d.Gaps) != 1 || !d.Gaps[0].Open {
		t.Errorf("an open gap should come through as open: %+v", d.Gaps)
	}
}

func TestAGapOutsideThePromptChangesNothing(t *testing.T) {
	st, pid := evidenceFixture(t, true)
	addGap(t, st, "07:00:00", "08:00:00")
	addGap(t, st, "10:00:00", "10:30:00")
	d := counted(t, st, pid)
	if d.Counts.NotCompared != 0 || d.Counts.Unverified != 2 || len(d.Gaps) != 0 {
		t.Errorf("counts = %+v, gaps %+v", d.Counts, d.Gaps)
	}
}

func TestNothingRecordingStaysNotCheckedEvenInAGap(t *testing.T) {
	st, pid := evidenceFixture(t, false)
	addGap(t, st, "09:00:00", "09:10:00")
	d := counted(t, st, pid)
	if d.Counts.Watched || d.Counts.NotCompared != 0 || d.Counts.Unverified != 0 {
		t.Errorf("counts = %+v", d.Counts)
	}
}

func TestPromptEvidenceRunningHasNoUnverified(t *testing.T) {
	ctx := context.Background()
	st, pid := evidenceFixture(t, true)
	st.DB.Exec(`UPDATE prompts SET ended_at = NULL, last_activity_at = ? WHERE id = ?`, "2026-10-17T11:58:00.000000000Z", pid)
	d, err := GetPromptDetail(ctx, st.DB, pid, evidenceNow)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Prompt.Running || d.Counts.Unverified != 0 || d.Counts.Pending != 2 {
		t.Errorf("running %v, counts %+v: the curl is pending, not unverified", d.Prompt.Running, d.Counts)
	}
	assertAccounting(t, d.Counts)
	// still running: its window reaches now, so the 09:30 command counts too
	if d.Counts.Background != 4 {
		t.Errorf("background = %d, want 4", d.Counts.Background)
	}
}

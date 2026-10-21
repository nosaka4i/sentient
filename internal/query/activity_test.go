package query

import (
	"context"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/store"
)

// addAct records an action; failed marks it the way a failing tool result does.
func addAct(t *testing.T, tx *store.Tx, sid, pid int64, ts, res, act, path, toolUse string, failed bool) {
	t.Helper()
	ctx := context.Background()
	if err := tx.AddAction(ctx, store.Action{SessionID: sid, PromptID: pid, TS: ts, Resource: res, Action: act, Path: path, Evidence: "log", ToolUseID: toolUse}); err != nil {
		t.Fatal(err)
	}
	if failed {
		if _, err := tx.ApplyToolResult(ctx, store.ToolResult{ToolUseID: toolUse, Failed: true}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestActivityKindsMatchTheVocabulary(t *testing.T) {
	if len(ActivityKinds) != 22 {
		t.Fatalf("%d kinds, want 22", len(ActivityKinds))
	}
	for _, r := range StatusRows {
		if !KnownKind(r[0], r[1]) {
			t.Errorf("status row %v is not an activity kind", r)
		}
	}
	if KnownKind("file", "explode") || KnownKind("", "") {
		t.Error("unknown kinds must not be known")
	}
}

func TestGetActivity(t *testing.T) {
	st, ids := seedPrompts(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	// add a failed command, a web fetch and a network connect
	tx, _ := st.Begin(ctx)
	ts := func(s string) string { return "2026-10-17T" + s + ".000000000Z" }
	addAct(t, tx, ids["s1"], ids["p2"], ts("10:10:00"), "command", "execute", "go vet ./...", "t4", true)
	addAct(t, tx, ids["s1"], ids["p2"], ts("10:11:00"), "web", "fetch", "https://developer.apple.com/docs/x", "t5", false)
	addAct(t, tx, ids["s2"], ids["p3"], ts("11:05:00"), "network", "connect", "github.com:443", "t6", false)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	a, err := GetActivity(ctx, st.DB, ActivityRequest{Days: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Rows) != 22 || a.Rows[0].Resource != "file" || a.Rows[21].Resource != "monitor" || a.HistoryBegins.IsZero() {
		t.Fatalf("rows = %d, first %+v", len(a.Rows), a.Rows[0])
	}
	row := func(res, act string) ActivityRow {
		for _, r := range a.Rows {
			if r.Resource == res && r.Action == act {
				return r
			}
		}
		t.Fatalf("no row %s %s", res, act)
		return ActivityRow{}
	}
	if r := row("prompt", "request"); r.Count != 4 || r.Repos != 2 {
		t.Errorf("prompt request = %+v", r)
	}
	if r := row("command", "execute"); r.Count != 2 || r.Failed != 1 || r.Summary != "1 failed" {
		t.Errorf("command execute = %+v", r)
	}
	if r := row("file", "update"); r.Count != 1 {
		t.Errorf("file update = %+v", r)
	}
	if r := row("web", "fetch"); r.Count != 1 || r.Hosts != "developer.apple.com" {
		t.Errorf("web fetch = %+v", r)
	}
	if r := row("network", "connect"); r.Count != 1 || r.Hosts != "github.com:443" {
		t.Errorf("network connect = %+v", r)
	}
	if r := row("file", "chmod"); r.Count != 0 {
		t.Errorf("chmod = %+v", r)
	}
	if r := row("subagent", "start"); r.Count != 1 {
		t.Errorf("subagent start = %+v", r)
	}
	// nothing asked for: the commands are listed, newest first
	if a.Resource != "command" || a.Action != "execute" || a.EventsTotal != 2 || len(a.Events) != 2 {
		t.Fatalf("default kind = %s/%s, %d of %d events", a.Resource, a.Action, len(a.Events), a.EventsTotal)
	}
	if e := a.Events[0]; e.What != "go vet ./..." || !e.Failed || e.PromptID != ids["p2"] || e.PromptText != "run the tests" || e.Agent != "claude" || e.Repo != "nosaka" || e.SessionID != "404dabcd" {
		t.Errorf("newest command = %+v", e)
	}
	if a.Events[1].Failed || a.Events[1].What != "go test" {
		t.Errorf("older command = %+v", a.Events[1])
	}

	// failed only, a limit, another kind, prompts as events
	f, _ := GetActivity(ctx, st.DB, ActivityRequest{Days: 1, Resource: "command", Action: "execute", FailedOnly: true}, now)
	if f.EventsTotal != 1 || len(f.Events) != 1 || !f.Events[0].Failed {
		t.Errorf("failed only = %+v", f.Events)
	}
	lim, _ := GetActivity(ctx, st.DB, ActivityRequest{Days: 1, Resource: "command", Action: "execute", Limit: 1}, now)
	if lim.EventsTotal != 2 || len(lim.Events) != 1 {
		t.Errorf("limit: %d of %d", len(lim.Events), lim.EventsTotal)
	}
	u, _ := GetActivity(ctx, st.DB, ActivityRequest{Days: 1, Resource: "file", Action: "update"}, now)
	if len(u.Events) != 1 || u.Events[0].What != "/w/nosaka/a.go" {
		t.Errorf("file update events = %+v", u.Events)
	}
	sub, _ := GetActivity(ctx, st.DB, ActivityRequest{Days: 1, Resource: "subagent", Action: "start"}, now)
	if len(sub.Events) != 1 || sub.Events[0].What != "Explore: find the crash" {
		t.Errorf("subagent events = %+v", sub.Events)
	}
	p, _ := GetActivity(ctx, st.DB, ActivityRequest{Days: 1, Resource: "prompt", Action: "request"}, now)
	if p.EventsTotal != 4 || len(p.Events) != 4 || p.Events[0].What != "100% done_ish?" || p.Events[0].SessionID != "9c1e0000" || p.Events[0].PromptID != ids["p4"] {
		t.Errorf("prompt events = %+v", p.Events)
	}

	// an unknown kind falls back; a window with nothing shows no events
	if x, _ := GetActivity(ctx, st.DB, ActivityRequest{Days: 1, Resource: "file", Action: "explode"}, now); x.Resource != "command" {
		t.Errorf("unknown kind fell back to %s", x.Resource)
	}
	old, _ := GetActivity(ctx, st.DB, ActivityRequest{Days: 1}, now.Add(72*time.Hour))
	if old.Resource != "" || len(old.Events) != 0 || old.Rows[0].Count != 0 {
		t.Errorf("quiet window = %+v", old)
	}
	// no commands but other activity: the first kind with events is listed
	if _, err := st.DB.Exec(`DELETE FROM actions WHERE resource = 'command'`); err != nil {
		t.Fatal(err)
	}
	if n, _ := GetActivity(ctx, st.DB, ActivityRequest{Days: 1}, now); n.Resource != "file" || n.Action != "update" {
		t.Errorf("fallback kind = %s/%s", n.Resource, n.Action)
	}
}

func TestActivitySearch(t *testing.T) {
	st, ids := seedPrompts(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	tx, _ := st.Begin(ctx)
	ts := func(s string) string { return "2026-10-17T" + s + ".000000000Z" }
	addAct(t, tx, ids["s1"], ids["p2"], ts("10:10:00"), "command", "execute", "grep -rn Snooze .", "g1", false)
	addAct(t, tx, ids["s1"], ids["p2"], ts("10:11:00"), "command", "execute", "GREP -c done_ish file", "g2", true)
	addAct(t, tx, ids["s1"], ids["p2"], ts("10:12:00"), "command", "execute", "echo 100% sure", "g3", false)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	search := func(kind [2]string, q string, failed bool) *Activity {
		t.Helper()
		a, err := GetActivity(ctx, st.DB, ActivityRequest{Days: 1, Resource: kind[0], Action: kind[1], Search: q, FailedOnly: failed}, now)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	cmd := [2]string{"command", "execute"}
	// case-insensitive, anywhere in the text; the count follows the filter, the tile counts don't
	a := search(cmd, "grep", false)
	if a.EventsTotal != 2 || len(a.Events) != 2 {
		t.Fatalf("grep: %d of %d", len(a.Events), a.EventsTotal)
	}
	for _, r := range a.Rows {
		if r.Resource == "command" && r.Count != 4 {
			t.Errorf("the tile still counts every command: %d", r.Count)
		}
	}
	// combined with failed only; whitespace is ignored
	if f := search(cmd, "  grep ", true); f.EventsTotal != 1 || !f.Events[0].Failed {
		t.Errorf("failed grep = %+v", f.Events)
	}
	// % and _ in the search match themselves, not any text
	if p := search(cmd, "100%", false); p.EventsTotal != 1 || p.Events[0].What != "echo 100% sure" {
		t.Errorf("100%% = %+v", p.Events)
	}
	if u := search(cmd, "done_ish", false); u.EventsTotal != 1 {
		t.Errorf("done_ish = %d", u.EventsTotal)
	}
	if x := search(cmd, "done-ish", false); x.EventsTotal != 0 {
		t.Errorf("_ must not match any character: %d", x.EventsTotal)
	}
	if n := search(cmd, "no such command", false); n.EventsTotal != 0 || len(n.Events) != 0 {
		t.Errorf("no match = %d", n.EventsTotal)
	}
	// the summary is searched too (subagent task), and prompts search their own text
	if s := search([2]string{"subagent", "start"}, "crash", false); s.EventsTotal != 1 {
		t.Errorf("subagent task search = %d", s.EventsTotal)
	}
	if p := search([2]string{"prompt", "request"}, "snooze", false); p.EventsTotal != 1 || p.Events[0].What != "Fix the Snooze crash" {
		t.Errorf("prompt search = %+v", p.Events)
	}
	if p := search([2]string{"prompt", "request"}, "100%", false); p.EventsTotal != 1 {
		t.Errorf("prompt 100%% = %d", p.EventsTotal)
	}
}

func TestFileReadsOutsideTheRepo(t *testing.T) {
	st, ids := seedPrompts(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	ts := "2026-10-17T10:30:00.000000000Z"
	addAct(t, tx, ids["s1"], ids["p2"], ts, "file", "read", "/w/nosaka/in.go", "r1", false)
	addAct(t, tx, ids["s1"], ids["p2"], ts, "file", "read", "/etc/hosts", "r2", false)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	a, err := GetActivity(ctx, st.DB, ActivityRequest{Days: 1, Resource: "file", Action: "read"}, time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if r := a.Rows[0]; r.Count != 2 || r.Outside != 1 || r.Summary != "1 outside repo" {
		t.Errorf("file read = %+v", r)
	}
}

func TestHostOfAndWhatOf(t *testing.T) {
	for in, want := range map[string]string{"https://a.example.com/x?y=1": "a.example.com", "github.com:443": "github.com:443", " localhost:3000 ": "localhost:3000", "": ""} {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
	if whatOf("file", "/a", "x") != "/a" || whatOf("web", "", "query") != "query" || whatOf("subagent", "Explore", "find") != "Explore: find" || whatOf("subagent", "", "find") != "find" {
		t.Error("whatOf")
	}
}

func TestGetActivityErrors(t *testing.T) {
	st, _ := seedPrompts(t)
	st.DB.Close()
	if _, err := GetActivity(context.Background(), st.DB, ActivityRequest{Days: 1}, time.Now()); err == nil {
		t.Error("a closed database should be an error")
	}
}

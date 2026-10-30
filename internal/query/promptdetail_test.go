package query

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/store"
)

func TestGetPromptDetail(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ts := func(s string) string { return "2026-10-17T" + s + ".000000000Z" }

	tx, _ := st.Begin(ctx)
	s1, _ := tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "404dabcd", Cwd: "/w/nosaka/ios", RepoRoot: "/w/nosaka"})
	p1, _ := tx.AddPrompt(ctx, s1, "make the build use staging", ts("09:00:00"))
	p2, _ := tx.AddPrompt(ctx, s1, "second prompt", ts("10:00:00"))
	raw := func(at, typ, rec string) int64 {
		id, err := tx.AppendRaw(ctx, store.RawRecord{SessionID: s1, TS: ts(at), SourceFile: "f", Type: typ, Record: rec})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	edit := raw("09:01:00", "assistant", `{"message":{"content":[{"type":"tool_use","id":"e1","name":"Edit","input":{"file_path":"/w/nosaka/ios/Config.plist","old_string":"api\nprod\n","new_string":"api\nstaging\nextra\n"}}]}}`)
	bash := raw("09:02:00", "assistant", `{"message":{"content":[{"type":"tool_use","id":"b1","name":"Bash","input":{"command":"cd ios && ls | sort"}}]}}`)
	write := raw("09:03:00", "assistant", `{"message":{"content":[{"type":"tool_use","id":"w1","name":"Write","input":{"file_path":"/home/u/.zshrc","content":"export A=1\nexport B=2\n"}}]}}`)
	multi := raw("09:04:00", "assistant", `{"message":{"content":[{"type":"tool_use","id":"m1","name":"MultiEdit","input":{"file_path":"/w/nosaka/ios/Config.plist","edits":[{"old_string":"a","new_string":"b"},{"old_string":"c","new_string":""}]}}]}}`)
	// The reply: the newest assistant record with text. A sidechain one is skipped,
	// and so is one after the next prompt.
	raw("09:05:00", "assistant", `{"message":{"content":[{"type":"text","text":"Done: updated `+"`Config.plist`"+`."}]}}`)
	raw("09:06:00", "assistant", `{"isSidechain":true,"message":{"content":[{"type":"text","text":"subagent chatter"}]}}`)
	raw("09:06:30", "assistant", `{"message":{"content":[{"type":"tool_use","id":"x","name":"Read","input":{}}]}}`)
	raw("10:01:00", "assistant", `{"message":{"content":[{"type":"text","text":"reply to the second prompt"}]}}`)

	act := func(at, res, action, path, tool string, part int, rawID int64) {
		if err := tx.AddAction(ctx, store.Action{SessionID: s1, PromptID: p1, TS: ts(at), Resource: res, Action: action, Path: path, Evidence: "log", RawRecordID: rawID, ToolUseID: tool, Part: part}); err != nil {
			t.Fatal(err)
		}
	}
	act("09:01:00", "file", "update", "/w/nosaka/ios/Config.plist", "e1", 0, edit)
	act("09:02:00", "command", "execute", "cd ios", "b1", 0, bash)
	act("09:02:00", "command", "execute", "ls", "b1", 1, bash)
	act("09:02:00", "command", "execute", "sort", "b1", 2, bash)
	act("09:03:00", "file", "create", "/home/u/.zshrc", "w1", 0, write)
	act("09:04:00", "file", "update", "/w/nosaka/ios/Config.plist", "m1", 0, multi)
	act("09:04:30", "file", "read", "/w/nosaka/a.txt", "r1", 0, 0)
	act("09:05:00", "subagent", "start", "general", "a1", 0, 0)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	st.DB.Exec(`UPDATE actions SET lines_added = 2, lines_removed = 1 WHERE tool_use_id = 'e1'`)
	st.DB.Exec(`UPDATE actions SET lines_added = 1, lines_removed = 1 WHERE tool_use_id = 'm1'`)
	st.DB.Exec(`UPDATE actions SET lines_added = 2 WHERE tool_use_id = 'w1'`)
	st.DB.Exec(`UPDATE actions SET failed = 1 WHERE tool_use_id = 'b1' AND part = 2`)

	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	d, err := GetPromptDetail(ctx, st.DB, p1, now)
	if err != nil {
		t.Fatal(err)
	}
	if d.Prompt.ID != p1 || d.Prompt.Text != "make the build use staging" || d.Prompt.Repo != "nosaka" || d.Prompt.SessionID != "404dabcd" || d.Prompt.Running {
		t.Errorf("prompt = %+v", d.Prompt)
	}
	if d.Prompt.Actions != 7 { // 3 commands + 3 file changes + 1 read; subagent start not counted
		t.Errorf("actions = %d, want 7", d.Prompt.Actions)
	}

	// Tiles, scoped to the prompt.
	if len(d.Activity) != len(StatusRows) {
		t.Fatalf("activity rows = %d", len(d.Activity))
	}
	byKey := map[string]ActivityRow{}
	for _, r := range d.Activity {
		byKey[r.Resource+" "+r.Action] = r
	}
	if r := byKey["prompt request"]; r.Count != 1 || r.Repos != 1 {
		t.Errorf("prompt tile = %+v", r)
	}
	if r := byKey["command execute"]; r.Count != 3 || r.Failed != 1 {
		t.Errorf("command tile = %+v", r)
	}
	if r := byKey["file update"]; r.Count != 2 || r.Added != 3 || r.Removed != 2 {
		t.Errorf("file update tile = %+v", r)
	}
	if r := byKey["file read"]; r.Count != 1 || r.Outside != 0 {
		t.Errorf("file read tile = %+v", r)
	}

	// Trace: the prompt, then every action in order (8 + 1).
	if len(d.Steps) != 9 {
		t.Fatalf("steps = %d, want 9", len(d.Steps))
	}
	if s := d.Steps[0]; s.N != 1 || s.Resource != "prompt" || s.Path != "make the build use staging" {
		t.Errorf("step 1 = %+v", s)
	}
	if s := d.Steps[1]; s.N != 2 || s.Offset != time.Minute || s.Path != "/w/nosaka/ios/Config.plist" {
		t.Errorf("step 2 = %+v", s)
	}
	cd, ls, sort := d.Steps[2], d.Steps[3], d.Steps[4]
	if cd.CallID != "b1" || cd.CallCommand != "cd ios && ls | sort" || !cd.Builtin {
		t.Errorf("cd step = %+v", cd)
	}
	if ls.CallID != "b1" || ls.CallCommand != "" || ls.Builtin {
		t.Errorf("ls step = %+v (only the first part carries the whole line)", ls)
	}
	if !sort.Failed {
		t.Errorf("sort step = %+v, want failed", sort)
	}
	if s := d.Steps[5]; s.CallID != "" {
		t.Errorf("write step %+v is not part of a call", s)
	}

	// Files: the plist's two edits merge; the .zshrc is outside the repo.
	if len(d.Files) != 2 {
		t.Fatalf("files = %+v", d.Files)
	}
	plist, rc := d.Files[0], d.Files[1]
	if plist.Path != "/w/nosaka/ios/Config.plist" || plist.Added != 3 || plist.Removed != 2 || plist.Outside || len(plist.Steps) != 2 || plist.By != "Edit" {
		t.Errorf("plist = %+v", plist)
	}
	wantDiff := []DiffLine{
		{"h", "Edit"}, {"d", "api"}, {"d", "prod"}, {"a", "api"}, {"a", "staging"}, {"a", "extra"},
		{"h", "edit 2: MultiEdit (2 changes)"}, {"d", "a"}, {"a", "b"}, {"d", "c"},
	}
	if len(plist.Diff) != len(wantDiff) {
		t.Fatalf("plist diff = %+v", plist.Diff)
	}
	for i, l := range wantDiff {
		if plist.Diff[i] != l {
			t.Errorf("diff[%d] = %+v, want %+v", i, plist.Diff[i], l)
		}
	}
	if rc.Path != "/home/u/.zshrc" || !rc.Outside || rc.Action != "create" || rc.By != "Write" || len(rc.Diff) != 3 || rc.Diff[1] != (DiffLine{"a", "export A=1"}) {
		t.Errorf("zshrc = %+v", rc)
	}

	// Reply: the last real assistant text inside this prompt.
	if d.Reply != "Done: updated `Config.plist`." || d.ReplyAt.Format("15:04:05") != "09:05:00" {
		t.Errorf("reply = %q at %v", d.Reply, d.ReplyAt)
	}

	// The second prompt has its own reply and no actions.
	d2, err := GetPromptDetail(ctx, st.DB, p2, now)
	if err != nil {
		t.Fatal(err)
	}
	if d2.Reply != "reply to the second prompt" || len(d2.Steps) != 1 || len(d2.Files) != 0 {
		t.Errorf("second prompt = reply %q steps %d files %d", d2.Reply, len(d2.Steps), len(d2.Files))
	}

	if _, err := GetPromptDetail(ctx, st.DB, 9999, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing prompt: err = %v, want ErrNotFound", err)
	}
}

func TestEditDiffEdgeCases(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// No stored record: a note instead of a diff.
	d, by := editDiff(ctx, st.DB, 0, "t", 1)
	if len(d) != 1 || d[0].Kind != "h" || by != "" {
		t.Errorf("missing record: %+v %q", d, by)
	}

	big := strings.Repeat("line\n", maxDiffLines+30)
	capped := capDiff(append([]DiffLine(nil), replaceLines("", big)...))
	if len(capped) != maxDiffLines+1 || capped[maxDiffLines].Text != "… 30 more lines" {
		t.Errorf("capped diff: %d lines, last %+v", len(capped), capped[len(capped)-1])
	}
	if got := capDiff([]DiffLine{{"a", "x"}}); len(got) != 1 {
		t.Errorf("short diff changed: %+v", got)
	}

	for cmd, want := range map[string]bool{
		"":                  false,
		"cd ios":            true,
		"echo hi":           true, // bash runs its own echo: no process for the kernel to see
		"printf %s x":       true,
		"pwd":               true,
		"GOFLAGS=-p=1 echo": true, // the assignment belongs to the command
		"A=1 B=2 test -f x": true,
		"/bin/echo hi":      false, // an explicit path runs the program
		"ls":                false,
		"command ls":        false, // runs a program
		"exec ls":           false, // replaces the shell: the kernel sees ls
		"A=1 go test":       false,
		"A=1":               false,
	} {
		if got := isBuiltin(cmd); got != want {
			t.Errorf("isBuiltin(%q) = %v, want %v", cmd, got, want)
		}
	}
}

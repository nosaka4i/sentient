package claudecode

import (
	"encoding/json"
	"testing"
)

func TestPromptText(t *testing.T) {
	human := &origin{Kind: "human"}
	msg := func(content string) *message { return &message{Role: "user", Content: json.RawMessage(content)} }
	cases := []struct {
		name string
		rec  record
		want string
		ok   bool
	}{
		{"typed string", record{Type: "user", Origin: human, Message: msg(`"  fix it \n"`)}, "fix it", true},
		{"no origin (older logs)", record{Type: "user", Message: msg(`"fix it"`)}, "fix it", true},
		{"text blocks", record{Type: "user", Message: msg(`[{"type":"text","text":"a"},{"type":"image"},{"type":"text","text":"b"}]`)}, "a\nb", true},
		{"tool result", record{Type: "user", Message: msg(`[{"type":"tool_result","tool_use_id":"x"}]`)}, "", false},
		{"meta", record{Type: "user", IsMeta: true, Message: msg(`"x"`)}, "", false},
		{"sidechain", record{Type: "user", IsSidechain: true, Message: msg(`"x"`)}, "", false},
		{"task notification", record{Type: "user", Origin: &origin{Kind: "task-notification"}, Message: msg(`"x"`)}, "", false},
		{"bang command", record{Type: "user", Message: msg(`"<bash-input>ls</bash-input>"`)}, "", false},
		{"bang output", record{Type: "user", Message: msg(`"<bash-stdout>x</bash-stdout><bash-stderr></bash-stderr>"`)}, "", false},
		{"slash command", record{Type: "user", Message: msg(`"<command-name>/clear</command-name>"`)}, "", false},
		{"interrupted", record{Type: "user", Message: msg(`[{"type":"text","text":"[Request interrupted by user]"}]`)}, "", false},
		{"empty", record{Type: "user", Message: msg(`"   "`)}, "", false},
		{"bad content", record{Type: "user", Message: msg(`42`)}, "", false},
		{"no message", record{Type: "user"}, "", false},
		{"assistant", record{Type: "assistant", Message: msg(`"x"`)}, "", false},
	}
	for _, c := range cases {
		got, ok := promptText(&c.rec)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got %q %v, want %q %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestMapTool(t *testing.T) {
	cases := []struct {
		name, input string
		want        mappedAction
		ok          bool
	}{
		{"Bash", `{"command":"ls","description":"list"}`, mappedAction{"command", "execute", "ls", "list"}, true},
		{"Edit", `{"file_path":"/a"}`, mappedAction{"file", "update", "/a", ""}, true},
		{"MultiEdit", `{"file_path":"/a"}`, mappedAction{"file", "update", "/a", ""}, true},
		{"NotebookEdit", `{"notebook_path":"/n.ipynb"}`, mappedAction{"file", "update", "/n.ipynb", ""}, true},
		{"Write", `{"file_path":"/w"}`, mappedAction{"file", "update", "/w", ""}, true},
		{"Read", `{"file_path":"/r"}`, mappedAction{"file", "read", "/r", ""}, true},
		{"WebFetch", `{"url":"https://x.dev/a"}`, mappedAction{"web", "fetch", "https://x.dev/a", ""}, true},
		{"WebSearch", `{"query":"q"}`, mappedAction{"web", "search", "q", ""}, true},
		{"Task", `{"subagent_type":"Explore","description":"d"}`, mappedAction{"subagent", "start", "Explore", "d"}, true},
		{"Agent", `{"subagent_type":"Plan"}`, mappedAction{"subagent", "start", "Plan", ""}, true},
		{"Read", `"__unparsed"`, mappedAction{"file", "read", "", ""}, true},
		{"TaskUpdate", `{}`, mappedAction{}, false},
	}
	for _, c := range cases {
		got := mapTool(c.name, json.RawMessage(c.input))
		if (len(got) == 1) != c.ok || (c.ok && got[0] != c.want) {
			t.Errorf("%s: got %+v, want %+v %v", c.name, got, c.want, c.ok)
		}
	}

	// A chained Bash command is one action per simple command.
	got := mapTool("Bash", json.RawMessage(`{"command":"cd x && ls | sort","description":"d"}`))
	want := []mappedAction{
		{"command", "execute", "cd x", "d"},
		{"command", "execute", "ls", "d"},
		{"command", "execute", "sort", "d"},
	}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("chained Bash = %+v", got)
	}
}

func TestResultDetails(t *testing.T) {
	cases := []struct {
		in       string
		verb     string
		add, rem int
	}{
		{``, "", 0, 0},
		{`"Error: x"`, "", 0, 0},
		{`{"bad`, "", 0, 0},
		{`{"structuredPatch":[{"lines":["-a","+b","+c"," d"]},{"lines":["-e"]}]}`, "", 2, 2},
		{`{"type":"create","content":"x\ny\nz\n","structuredPatch":[]}`, "create", 3, 0},
		{`{"type":"update","structuredPatch":[{"lines":["+n"]}]}`, "update", 1, 0},
		{`{"type":"text"}`, "", 0, 0},
	}
	for _, c := range cases {
		verb, add, rem := resultDetails(json.RawMessage(c.in))
		if verb != c.verb || add != c.add || rem != c.rem {
			t.Errorf("resultDetails(%s) = %q %d %d, want %q %d %d", c.in, verb, add, rem, c.verb, c.add, c.rem)
		}
	}
}

func TestNormTime(t *testing.T) {
	if got := normTime("2026-10-17T09:00:00.5Z"); got != "2026-10-17T09:00:00.500000000Z" {
		t.Errorf("normTime = %q", got)
	}
	if normTime("") != "" || normTime("yesterday") != "" {
		t.Error("normTime should return empty for missing/bad input")
	}
}

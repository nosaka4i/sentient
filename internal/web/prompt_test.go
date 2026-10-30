package web

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/query"
)

func sampleDetail() *query.PromptDetail {
	started := now.Add(-30 * time.Minute)
	rows := make([]query.ActivityRow, 0, len(query.StatusRows))
	for _, r := range query.StatusRows {
		rows = append(rows, query.ActivityRow{Resource: r[0], Action: r[1]})
	}
	rows[0].Count, rows[0].Repos = 1, 1
	rows[5].Count, rows[5].Failed = 3, 1
	return &query.PromptDetail{
		HistoryBegins: now.Add(-48 * time.Hour),
		RepoRoot:      "/w/nosaka",
		Prompt: query.PromptRow{
			ID: 7, Text: "make the build <use> staging\nand then tell me", Started: started, Duration: 12 * time.Minute,
			Agent: "claude", Repo: "nosaka", SessionID: "404dabcd", Actions: 5,
		},
		Activity: rows,
		Files: []query.FileChange{
			{Path: "/w/nosaka/ios/Config.plist", Action: "update", Added: 2, Removed: 1, Steps: []int64{2}, By: "Edit", First: started.Add(time.Minute),
				Diff: []query.DiffLine{{Kind: "h", Text: "Edit"}, {Kind: "d", Text: "<old>"}, {Kind: "a", Text: "new"}}},
			{Path: "/home/u/.zshrc", Action: "create", Added: 1, Outside: true, Steps: []int64{6, 9}, By: "Write", First: started.Add(5 * time.Minute),
				Diff: []query.DiffLine{{Kind: "c", Text: "same"}}},
		},
		Steps: []query.Step{
			{N: 1, Resource: "prompt", Action: "request", Path: "make the build", Evidence: "log"},
			{N: 2, Offset: time.Minute, Resource: "file", Action: "update", Path: "/w/nosaka/ios/Config.plist", Evidence: "log"},
			{N: 3, Offset: 2 * time.Minute, Resource: "command", Action: "execute", Path: "cd ios", Evidence: "log", CallID: "b1", CallCommand: "cd ios && ls", Builtin: true},
			{N: 4, Resource: "command", Action: "execute", Path: "ls", Failed: true, Evidence: "log", CallID: "b1"},
			{N: 5, Offset: 3 * time.Minute, Resource: "command", Action: "execute", Path: "pwd", Evidence: "log", Unverified: true},
			{N: 6, Offset: 4 * time.Minute, Resource: "command", Action: "execute", Path: "id", Evidence: "kernel", Unreported: true},
			{N: 7, Offset: 6 * time.Minute, Resource: "file", Action: "update", Path: "/home/u/.zshrc", Evidence: "both", Alert: true},
		},
		Counts:  query.EvidenceCounts{Watched: true, AgentLog: 6, KernelLog: 3, Confirmed: 2, Unverified: 1, NotChecked: 2, Pending: 1, Unreported: 1, Background: 47, AgentID: "root/a"},
		Reply:   "Done: changed `Config.plist` <fast>.\n\nSecond paragraph.",
		ReplyAt: started.Add(12 * time.Minute),
	}
}

func TestPromptPage(t *testing.T) {
	src := &fakeSource{detail: sampleDetail()}
	s := newTestServer(t, src)
	s.home = "/home/u"
	resp, body := get(t, s, "/prompt/7", "localhost:7357")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d\n%s", resp.StatusCode, body)
	}
	if src.asked != 7 {
		t.Errorf("asked for prompt %d, want 7", src.asked)
	}
	for _, want := range []string{
		`<title>sentient · prompt</title>`,
		`<a class="back" href="/">&larr; Dashboard</a>`,
		`make the build &lt;use&gt; staging`, // the first line, escaped
		`<summary>Show full prompt</summary><pre>make the build &lt;use&gt; staging`,
		`<span class="repo">nosaka</span>`,
		`<a class="sess" href="/session/404dabcd"`,
		`&ndash;`, // the end time
		`<span class="state-done">`,
		// the agent log / kernel log / background tiles, with their exceptions called out
		`<span class="lbl"><b>agent log</b></span>
        <span class="num">6</span>`,
		`>1 unverified</span>`,
		`>2 not checked</span>`,
		`>1 pending</span>`,
		`<span class="lbl"><b>kernel log</b></span>
        <span class="num">3</span>`,
		`<span class="tag unrep">1 unreported</span>`,
		// background commands are not a tile: one quiet line links to the agent's timeline for the prompt
		`<a href="/agent/root%2Fa?from=`,
		`47 background commands</a> ran while the agent worked on this prompt`,
		`<a class="tile zero" href="#trace">`, // zero tiles link to the trace (and are hidden)
		`href="#files"`,                       // the file tiles open the files drawer
		`<a class="sublink warn" href="#trace">1 failed</a>`,
		// files: the first is open, repo-relative path, diff escaped; the second is outside
		`<details open>`,
		`<span class="fpath">ios/Config.plist</span>`,
		`<span class="d">-&lt;old&gt;</span><span class="a">&#43;new</span>`, // html/template writes + as &#43;
		`<span class="fpath">~/.zshrc <span class="tag outside">outside repo</span></span>`,
		`step 2 · `,
		`steps 6, 9 · `,
		`by Edit tool`,
		`<span class="h">@@ Edit @@</span>`,
		`<span class=""> same</span>`,
		// trace: collapsed, a call header with its commands nested, builtin noted, failure tagged
		`Show all 7 steps`,
		`<li class="step unclaimed">`,
		`<span class="note">· not in the agent&#39;s log</span>`,
		`<span class="note">· not seen by the kernel</span>`,
		`<li class="step flagged">`,
		// the files are in a drawer, closed
		`id="files-toggle"`,
		`Show 2 files`,
		`<div class="trace-body" id="files-body" hidden>`,
		`<span class="p">cd ios &amp;&amp; ls <span class="note">· 1 tool call, 2 commands</span></span>`,
		`<span class="note">· shell builtin, never execs</span>`,
		`<span class="tag fail">failed</span>`,
		`<span class="t">&#43;1:00</span>`,
		// reply
		`<code>Config.plist</code> &lt;fast&gt;.`,
		`<p>Second paragraph.</p>`,
		`id="reply-toggle"`,
		`<span class="hint">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if t.Failed() {
		t.Log(body)
	}
	if strings.Contains(body, `<meta http-equiv="refresh"`) {
		t.Error("a finished prompt must not auto-reload")
	}
	if strings.Contains(body, "<use>") || strings.Contains(body, "<fast>") {
		t.Error("agent text was not escaped")
	}
}

func TestPromptPageRunningAndSparse(t *testing.T) {
	d := sampleDetail()
	d.Prompt.Running = true
	d.Prompt.Text = "short prompt"
	d.Prompt.Repo = ""
	d.Files = nil
	d.Reply = ""
	d.Steps = d.Steps[:1]
	s := newTestServer(t, &fakeSource{detail: d})
	_, body := get(t, s, "/prompt/7", "localhost")
	for _, want := range []string{
		`<span class="state-running">`,
		`Show all 1 steps`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	for _, bad := range []string{`Files changed`, `Agent's reply`, `Show full prompt`, `class="repo"`, `<meta http-equiv="refresh"`} {
		if strings.Contains(body, bad) {
			t.Errorf("page should not contain %q", bad)
		}
	}
}

func TestPromptNotFound(t *testing.T) {
	for _, tc := range []struct {
		path string
		err  error
		want int
	}{
		{"/prompt/9", api.ErrNotFound, http.StatusNotFound},
		{"/prompt/abc", nil, http.StatusNotFound},
		{"/prompt/0", nil, http.StatusNotFound},
		{"/prompt/9", api.ErrUnreachable, http.StatusServiceUnavailable},
		{"/prompt/9", errors.New("boom"), http.StatusServiceUnavailable},
	} {
		s := newTestServer(t, &fakeSource{err: tc.err})
		if resp, _ := get(t, s, tc.path, "localhost"); resp.StatusCode != tc.want {
			t.Errorf("%s with %v: status %d, want %d", tc.path, tc.err, resp.StatusCode, tc.want)
		}
	}
}

func TestDisplayPath(t *testing.T) {
	for _, tc := range []struct{ path, repo, home, want string }{
		{"/w/r/ios/a.txt", "/w/r", "/home/u", "ios/a.txt"},
		{"/home/u/.zshrc", "/w/r", "/home/u", "~/.zshrc"},
		{"/etc/hosts", "/w/r", "/home/u", "/etc/hosts"},
		{"/w/other/x", "/w/r", "", "/w/other/x"},
		{"/home/u/p", "", "/home/u", "~/p"},
	} {
		if got := DisplayPath(tc.path, tc.repo, tc.home); got != tc.want {
			t.Errorf("DisplayPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestBuildTraceCapsLongPrompts(t *testing.T) {
	steps := make([]query.Step, maxTraceSteps+25)
	for i := range steps {
		steps[i] = query.Step{N: int64(i + 1), Resource: "file", Action: "read", Path: "/a", Evidence: "log"}
	}
	rows, n, more := buildTrace(steps)
	if len(rows) != maxTraceSteps || n != maxTraceSteps || more != 25 {
		t.Errorf("rows %d steps %d more %d", len(rows), n, more)
	}
}

func TestOffsetLabelAndReply(t *testing.T) {
	if got := offsetLabel(75 * time.Second); got != "+1:15" {
		t.Errorf("offsetLabel = %q", got)
	}
	got := replyParagraphs("one `a<b>`\n\n\n\ntwo")
	if len(got) != 2 || string(got[0]) != "one <code>a&lt;b&gt;</code>" || string(got[1]) != "two" {
		t.Errorf("replyParagraphs = %q", got)
	}
}

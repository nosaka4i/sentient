package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/query"
)

func sampleSession() *query.SessionDetail {
	started := now.Add(-90 * time.Minute)
	rows := make([]query.ActivityRow, 0, len(query.StatusRows))
	for _, r := range query.StatusRows {
		rows = append(rows, query.ActivityRow{Resource: r[0], Action: r[1]})
	}
	rows[0].Count = 2
	rows[10].Count = 1
	p1 := promptRow(1, "first <i>prompt</i>", started, "claude", "nosaka")
	p2 := promptRow(2, "second", started.Add(30*time.Minute), "claude", "nosaka")
	p2.Flags = 2
	p2.Running = true
	return &query.SessionDetail{
		HistoryBegins: now.Add(-48 * time.Hour),
		ID:            "404dabcd-1111", Agent: "claude", AgentVersion: "2.1.3", Repo: "nosaka", Cwd: "/home/u/work/nosaka",
		Started: started, LastActivity: started.Add(80 * time.Minute), Running: true, Actions: 6, Flags: 2,
		// 9 prompts in all; the newest two are loaded, newest first
		TotalPrompts: 9, FirstPrompt: p1.Text,
		Prompts:   []query.PromptRow{p2, p1},
		Activity:  rows,
		Subagents: []query.Subagent{{Kind: "Explore", Task: "find the <crash>", At: started.Add(time.Minute), PromptNumber: 1}, {At: started.Add(2 * time.Minute)}},
	}
}

func TestSessionPage(t *testing.T) {
	src := &fakeSource{session: sampleSession()}
	s := newTestServer(t, src)
	s.home = "/home/u"
	resp, body := get(t, s, "/session/404dabcd-1111", "localhost:7357")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d\n%s", resp.StatusCode, body)
	}
	if src.askedSession != "404dabcd-1111" {
		t.Errorf("asked for session %q", src.askedSession)
	}
	for _, want := range []string{
		`<title>sentient · session 404d</title>`,
		`<h1 title="404dabcd-1111">404d</h1>`,
		`started with</b> &ldquo;first &lt;i&gt;prompt&lt;/i&gt;&rdquo;`,
		`<span class="mono">2.1.3</span>`,
		`<span>9 prompts</span>`,
		`<span>6 actions</span>`,
		`<span class="flag" title="2 attention items">2 flags</span>`,
		`<span class="state-running">`,
		`<span class="cwd" title="Working directory">~/work/nosaka</span>`,
		`6 actions in this session`,
		`<a class="tile zero" href="#prompts">`,
		`<a class="tile" href="#subagents">`,
		// numbered from the session's first prompt, so the newest is #9
		`<td class="n">9</td>`,
		`<td class="n">8</td>`,
		`href="/prompt/2"`,
		`newest 2 of 9`,
		`<a class="chip" href="/prompts?session=404dabcd-1111">See all 9 prompts &rarr;</a>`,
		`7 older, with search and sorting`,
		`Subagents <span class="count">(2)</span>`,
		`<span class="task">find the &lt;crash&gt;</span>`,
		`<span class="tag">Explore</span>`,
		`<span>prompt 1</span>`,
		`(no description)`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, `<meta http-equiv="refresh"`) {
		t.Error("a running session must not reload itself: it would fold away what the reader opened")
	}
	if t.Failed() {
		t.Log(body)
	}
}

func TestSessionPageEndedAndEmpty(t *testing.T) {
	d := sampleSession()
	d.Running = false
	d.Subagents = nil
	d.Flags = 0
	d.Repo, d.Cwd = "", ""
	d.Started, d.LastActivity = time.Time{}, time.Time{}
	d.Prompts, d.TotalPrompts, d.FirstPrompt = nil, 0, ""
	s := newTestServer(t, &fakeSource{session: d})
	_, body := get(t, s, "/session/x", "localhost")
	for _, want := range []string{`<span class="state-done">`, `No prompts recorded for this session.`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	for _, bad := range []string{`Subagents`, `<meta http-equiv="refresh"`, `class="cwd"`, `started with`, `See all`} {
		if strings.Contains(body, bad) {
			t.Errorf("page should not contain %q", bad)
		}
	}
}

func TestSessionNotFound(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{api.ErrNotFound, http.StatusNotFound},
		{nil, http.StatusNotFound}, // a nil result is "not found" too
		{api.ErrUnreachable, http.StatusServiceUnavailable},
	} {
		s := newTestServer(t, &fakeSource{err: tc.err})
		if resp, _ := get(t, s, "/session/nope", "localhost"); resp.StatusCode != tc.want {
			t.Errorf("err %v: status %d, want %d", tc.err, resp.StatusCode, tc.want)
		}
	}
}

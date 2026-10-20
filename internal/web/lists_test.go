package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/query"
)

func promptRow(id int64, text string, started time.Time, agent, repo string) query.PromptRow {
	return query.PromptRow{
		ID: id, Text: text, Started: started, Duration: 5 * time.Minute,
		Agent: agent, Repo: repo, SessionID: "404dabcd", Actions: 3,
	}
}

func sampleGroups() *query.SessionGroups {
	flagged := promptRow(3, "yesterday one", now.Add(-26*time.Hour), "claude", "nosaka")
	flagged.Flags = 1
	other := promptRow(4, "today two", now.Add(-2*time.Hour), "codex", "sentient")
	other.SessionID = "9c1e0000"
	return &query.SessionGroups{
		HistoryBegins: now.Add(-72 * time.Hour),
		TotalPrompts:  135,
		TotalSessions: 5,
		Agents:        []string{"claude", "codex"},
		Repos:         []string{"nosaka", "sentient"},
		Sessions: []query.SessionOption{
			{ID: "404dabcd", Agent: "claude", Repo: "nosaka"},
			{ID: "9c1e0000", Agent: "codex", Repo: "sentient"},
			{ID: "7be2aaaa", Agent: "claude"},
		},
		OlderSessions: 2,
		Groups: []query.SessionGroup{
			{
				Session: query.SessionSummary{ID: "404dabcd", Agent: "claude", Repo: "nosaka", Prompts: 128,
					Started: now.Add(-5 * 24 * time.Hour), LastActivity: now.Add(-time.Hour)},
				Matching: 128,
				Prompts: []query.PromptRow{
					promptRow(5, "today <b>one</b>", now.Add(-time.Hour), "claude", "nosaka"),
					flagged,
					promptRow(2, "older", now.Add(-5*24*time.Hour), "claude", "nosaka"),
				},
			},
			{
				Session:  query.SessionSummary{ID: "9c1e0000", Agent: "codex", Repo: "sentient", Prompts: 1, Started: now.Add(-2 * time.Hour), LastActivity: now.Add(-2*time.Hour + 5*time.Minute)},
				Matching: 1,
				Prompts:  []query.PromptRow{other},
			},
		},
	}
}

func sampleOneSession() *query.PromptList {
	return &query.PromptList{
		HistoryBegins: now.Add(-72 * time.Hour),
		Total:         9,
		Agents:        []string{"claude", "codex"},
		Repos:         []string{"nosaka", "sentient"},
		Session: &query.SessionSummary{ID: "404dabcd", Agent: "claude", Repo: "nosaka", Prompts: 9, Actions: 40, Flags: 2,
			Started: now.Add(-3 * time.Hour), LastActivity: now.Add(-time.Hour)},
		Sessions: []query.SessionOption{{ID: "9c1e0000", Agent: "codex", Repo: "sentient"}, {ID: "7be2aaaa", Agent: "claude"}},
		Prompts: []query.PromptRow{
			promptRow(5, "first <i>shown</i>", now.Add(-time.Hour), "claude", "nosaka"),
			promptRow(4, "second shown", now.Add(-2*time.Hour), "claude", "nosaka"),
		},
	}
}

func TestPromptsPageAllSessions(t *testing.T) {
	src := &fakeSource{groups: sampleGroups()}
	s := newTestServer(t, src)
	resp, body := get(t, s, "/prompts?q=snooze&agent=claude&flagged=1&n=20", "localhost:7357")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d\n%s", resp.StatusCode, body)
	}
	if f := src.groupFilter; f.Text != "snooze" || f.Agent != "claude" || !f.Flagged || f.Limit != 20 || f.SessionID != "" || src.perSession != 3 {
		t.Errorf("group request = %+v per %d", src.groupFilter, src.perSession)
	}
	for _, want := range []string{
		`<title>sentient · prompts</title>`,
		`Prompts <span class="count">135</span>`,
		`name="q" value="snooze"`,
		`<option value="claude" selected>claude</option>`,
		// the Session menu: All sessions first, recent sessions as "id · repo",
		// a repo-less session falls back to the agent, older ones are noted
		`<option value="">All sessions</option>`,
		`<option value="404dabcd">404d · nosaka</option>`,
		`<option value="9c1e0000" disabled>9c1e · sentient</option>`, // codex doesn't match agent=claude
		`<option value="7be2aaaa">7be2 · claude</option>`,
		`<option disabled>2 older sessions…</option>`,
		`<input type="hidden" name="flagged" value="1">`,
		`aria-pressed="true"`,
		`2 of 5 sessions · 135 prompts match`,
		`clear filters`,
		// one group per session, newest first; no session/agent/repo columns
		`<a class="sid" href="/session/404dabcd" title="Open session 404dabcd">404d</a>`,
		`<span class="repo">nosaka</span><span class="dmeta">Oct 2 15:40 – Oct 7 14:40 · 128 of 128 prompts match · <a class="sid" href="/prompts?agent=claude&amp;flagged=1&amp;q=snooze&amp;session=404dabcd">newest 3 shown, see all &rarr;</a></span>`,
		`<span class="repo">sentient</span><span class="dmeta">Oct 7 13:40–13:45 · 1 of 1 prompt match</span>`,
		`today &lt;b&gt;one&lt;/b&gt;`,
		`<span class="flag" title="1 attention item">1</span>`,
		// 3 more sessions than the 2 shown: a link that asks for ten more than that
		`href="/prompts?agent=claude&amp;flagged=1&amp;n=12&amp;q=snooze"`,
		`3 older sessions`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	for _, bad := range []string{`<th>Session</th>`, `<th>Agent</th>`, `<th>Repo</th>`, `class="session"`, `class="sumline"`} {
		if strings.Contains(body, bad) {
			t.Errorf("page should not contain %q", bad)
		}
	}
	if strings.Index(body, "404d</a>") > strings.Index(body, "9c1e</a>") {
		t.Error("the session with the newest prompt must come first")
	}
	if t.Failed() {
		t.Log(body)
	}
}

func TestPromptsPageOneSession(t *testing.T) {
	src := &fakeSource{list: sampleOneSession()}
	s := newTestServer(t, src)
	resp, body := get(t, s, "/prompts?session=404dabcd&q=shown&n=100", "localhost:7357")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d\n%s", resp.StatusCode, body)
	}
	if f := src.filter; f.SessionID != "404dabcd" || f.Text != "shown" || f.Limit != 100 {
		t.Errorf("filter = %+v", f)
	}
	for _, want := range []string{
		// the selected session leads the menu data; it is in the list of recent ones? no: appended
		`<option value="404dabcd" selected>404d · nosaka</option>`,
		`<option value="9c1e0000">9c1e · sentient</option>`,
		`<div class="sumline">`,
		`<span class="repo">nosaka</span>`,
		`<span>9 prompts</span>`,
		`<span>40 actions</span>`,
		`<span class="flag">2 flags</span>`,
		`<a class="open" href="/session/404dabcd">Open session 404d &rarr;</a>`,
		`first &lt;i&gt;shown&lt;/i&gt;`,
		`2 of 9 match`,
		`<a href="/prompts?session=404dabcd">clear filters</a>`, // drops the search, keeps the session
		// 7 more prompts: ask for 50 more than the 2 shown
		`href="/prompts?n=52&amp;q=shown&amp;session=404dabcd"`,
		`7 older prompts in this session`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, `class="dmeta"`) {
		t.Error("a single session has no group header bars")
	}
	if t.Failed() {
		t.Log(body)
	}
}

func TestPromptsPageSessionFallsBackToAll(t *testing.T) {
	// A session the filters exclude (here: its agent isn't the chosen one)
	// shows the default view instead of an empty page.
	src := &fakeSource{list: sampleOneSession(), groups: sampleGroups()}
	s := newTestServer(t, src)
	_, body := get(t, s, "/prompts?session=404dabcd&agent=codex", "localhost")
	if !strings.Contains(body, `<a class="sid" href="/session/9c1e0000"`) || strings.Contains(body, `class="sumline"`) {
		t.Error("expected the all-sessions view")
	}
	if src.groupFilter.Agent != "codex" || src.groupFilter.SessionID != "" {
		t.Errorf("group request = %+v", src.groupFilter)
	}

	// An unknown session (no summary) falls back too.
	src = &fakeSource{list: &query.PromptList{}, groups: sampleGroups()}
	s = newTestServer(t, src)
	if resp, body := get(t, s, "/prompts?session=nope", "localhost"); resp.StatusCode != http.StatusOK || !strings.Contains(body, `class="sid"`) {
		t.Errorf("unknown session: status %d", resp.StatusCode)
	}
}

func TestPromptsPageDefaultsAndEmpty(t *testing.T) {
	src := &fakeSource{groups: &query.SessionGroups{}}
	s := newTestServer(t, src)
	_, body := get(t, s, "/prompts", "localhost")
	if src.groupFilter.Limit != query.DefaultSessionLimit || src.groupFilter.Text != "" {
		t.Errorf("default request = %+v", src.groupFilter)
	}
	for _, want := range []string{`No prompts yet.`, `0 of 0 sessions`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, "clear filters") || strings.Contains(body, "Show more") || strings.Contains(body, `<table`) {
		t.Error("an empty, unfiltered list shows no table, clear link or more link")
	}

	_, body = get(t, s, "/prompts?q=zzz&n=junk&agent=x", "localhost")
	if src.groupFilter.Limit != query.DefaultSessionLimit || !strings.Contains(body, "No prompts match these filters.") {
		t.Errorf("filtered empty: limit %d\n%s", src.groupFilter.Limit, body)
	}

	// n is capped; long search text is clipped.
	get(t, s, "/prompts?n=999999&q="+strings.Repeat("a", 500), "localhost")
	if src.groupFilter.Limit != query.MaxSessionLimit || len(src.groupFilter.Text) != 200 {
		t.Errorf("clamps: limit %d text %d", src.groupFilter.Limit, len(src.groupFilter.Text))
	}

	down := newTestServer(t, &fakeSource{err: api.ErrUnreachable})
	for _, path := range []string{"/prompts", "/prompts?session=x"} {
		if resp, _ := get(t, down, path, "localhost"); resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s unreachable: %d", path, resp.StatusCode)
		}
	}
}

func TestPromptsPageOneSessionEmptyAndSelectedOlder(t *testing.T) {
	l := sampleOneSession()
	l.Prompts, l.Total = nil, 0
	l.Sessions = nil // the session isn't among the recent ones: it is still offered
	s := newTestServer(t, &fakeSource{list: l})
	_, body := get(t, s, "/prompts?session=404dabcd&flagged=1", "localhost")
	for _, want := range []string{
		`<option value="404dabcd" selected>404d · nosaka</option>`,
		`No prompts in this session match these filters.`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestListHrefAndHelpers(t *testing.T) {
	if got := listHref(query.PromptFilter{}, "", false, 0); got != "/prompts" {
		t.Errorf("listHref empty = %q", got)
	}
	f := query.PromptFilter{Text: "a b", Agent: "claude", Repo: "r"}
	if got := listHref(f, "sid", true, 100); got != "/prompts?agent=claude&flagged=1&n=100&q=a+b&repo=r&session=sid" {
		t.Errorf("listHref = %q", got)
	}
	if sessionLabel("404dabcd", "", "claude") != "404d · claude" || sessionLabel("404dabcd", "r", "claude") != "404d · r" {
		t.Error("sessionLabel")
	}
	if pageSize(0, 10, 200) != 10 || pageSize(-3, 10, 200) != 10 || pageSize(50, 10, 200) != 50 || pageSize(999, 10, 200) != 200 {
		t.Error("pageSize")
	}
	at := time.Date(2026, 10, 7, 14, 0, 0, 0, now.Location())
	if got := timeRange(at, at.Add(30*time.Second), now); got != "Oct 7 14:00" {
		t.Errorf("short range = %q", got)
	}
	if got := timeRange(at, at.Add(90*time.Minute), now); got != "Oct 7 14:00–15:30" {
		t.Errorf("same-day range = %q", got)
	}
	if got := timeRange(at, at.Add(30*time.Hour), now); got != "Oct 7 14:00 – Oct 8 20:00" {
		t.Errorf("multi-day range = %q", got)
	}
	if sinceOf(time.Time{}, now) != nil {
		t.Error("sinceOf of zero should be nil")
	}
}

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
		Prompts:   []query.PromptRow{p1, p2},
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
		`<span>2 prompts</span>`,
		`<span>6 actions</span>`,
		`<span class="flag" title="2 attention items">2 flags</span>`,
		`<span class="state-running">`,
		`<span class="cwd" title="Working directory">~/work/nosaka</span>`,
		`6 actions in this session`,
		`<a class="tile zero" href="#prompts">`,
		`<a class="tile" href="#subagents">`,
		`<td class="n">1</td>`,
		`<td class="n">2</td>`,
		`href="/prompt/2"`,
		`Subagents <span class="count">(2)</span>`,
		`<span class="task">find the &lt;crash&gt;</span>`,
		`<span class="tag">Explore</span>`,
		`<span>prompt 1</span>`,
		`(no description)`,
		`<meta http-equiv="refresh"`, // running
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
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
	d.Prompts = nil
	s := newTestServer(t, &fakeSource{session: d})
	_, body := get(t, s, "/session/x", "localhost")
	for _, want := range []string{`<span class="state-done">`, `No prompts recorded for this session.`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	for _, bad := range []string{`Subagents`, `<meta http-equiv="refresh"`, `class="cwd"`, `started with`} {
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

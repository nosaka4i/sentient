package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/query"
)

func sampleAgents() []query.AgentRow {
	return []query.AgentRow{
		{ID: "root/a", Agent: "claude", Repo: "sentient", Version: "2.1.305", Started: now.Add(-2 * time.Hour), Running: true, Background: 1204},
		{ID: "root-b", Agent: "claude", Repo: "nosaka", Version: "2.1.304", Started: now.Add(-30 * time.Hour), Stopped: now.Add(-26 * time.Hour), Background: 8310},
	}
}

func sampleAgentDetail() *query.AgentDetail {
	return &query.AgentDetail{
		AgentRow: sampleAgents()[0], Dir: "/w/sentient",
		Sessions: []query.AgentSession{{ID: "404dabcd"}},
		Groups: []query.BackgroundGroup{
			{Key: "git status", Count: 560, First: now.Add(-2 * time.Hour), Last: now.Add(-time.Minute),
				Latest: []query.BackgroundRun{{At: now.Add(-time.Minute), Command: "git -c a=b status --porcelain", Key: "git status"}}},
			{Key: "claude -c", Count: 1, First: now.Add(-time.Hour), Last: now.Add(-time.Hour),
				Latest: []query.BackgroundRun{{At: now.Add(-time.Hour), Command: "2.1.305 -c", Key: "claude -c"}}},
		},
	}
}

func sampleTimeline() *query.Timeline {
	return &query.Timeline{Total: 250, Runs: []query.BackgroundRun{
		{At: now.Add(-time.Minute), Command: "git -c core.askPass= config --get remote.origin.url", Key: "git config --get"},
		{At: now.Add(-26 * time.Hour), Command: "git status", Key: "git status"},
	}}
}

func TestAgentsPage(t *testing.T) {
	src := &fakeSource{agents: sampleAgents()}
	resp, body := get(t, newTestServer(t, src), "/agents", "localhost")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{
		`Background commands run by the agent.`,
		`Agent processes <span class="count">(2)</span>`,
		`<span>Agent · repo</span><span>Started</span><span>Stopped</span><span class="r">Background commands</span>`,
		`<a class="arow" href="/agent/root%2Fa">`,
		`<span class="okdot" title="running"></span>`, // the running agent
		`<span class="state-ok">running</span>`,
		`<span class="where">nosaka</span>`,
		`<span class="r">1,204</span>`,
		`<span class="r">8,310</span>`,
		`<a href="/agents" aria-current="page">Agents</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, `<meta http-equiv="refresh"`) {
		t.Error("the page must not reload itself")
	}

	_, body = get(t, newTestServer(t, &fakeSource{}), "/agents", "localhost")
	if !strings.Contains(body, `No agent process has been seen yet`) {
		t.Error("no empty state")
	}
}

func TestAgentsPageDown(t *testing.T) {
	resp, _ := get(t, newTestServer(t, &fakeSource{err: api.ErrUnreachable}), "/agents", "localhost")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", resp.StatusCode)
	}
}

func TestAgentPageByCommand(t *testing.T) {
	src := &fakeSource{agent: sampleAgentDetail()}
	resp, body := get(t, newTestServer(t, src), "/agent/root%2Fa", "localhost")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d\n%s", resp.StatusCode, body)
	}
	if src.askedAgent != "root/a" {
		t.Errorf("asked for %q", src.askedAgent)
	}
	for _, want := range []string{
		`<a class="back" href="/agents">&larr; Agents</a>`,
		`claude · sentient`,
		`running since`,
		`<a class="sess" href="/session/404dabcd">404d</a>`,
		`Background commands <span class="count">(1,204)</span>`,
		`<a href="/agent/root%2Fa" aria-pressed="true">By command</a>`,
		`<a href="/agent/root%2Fa?view=timeline">Timeline</a>`,
		`<li class="bg">`,
		`<span class="chev">&#9656;</span>git status <span class="note">&middot; 560 times</span>`,
		`<span class="note">&middot; once</span>`,
		`<li><span class="t">Oct 7 15:39:00</span>git -c a=b status --porcelain`, // a run's time has its date
		`Oct 7 13:40–15:39`, // a group's range: the date once, then the end time
		`&hellip; 559 more`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, `<meta http-equiv="refresh"`) {
		t.Error("the page must not reload itself")
	}
	if src.timelineReq.Limit != 0 {
		t.Error("the per-command view must not ask for the timeline")
	}
}

func TestAgentPageStoppedAndEmpty(t *testing.T) {
	d := sampleAgentDetail()
	d.AgentRow = sampleAgents()[1]
	d.Groups, d.Sessions, d.Background = nil, nil, 0
	_, body := get(t, newTestServer(t, &fakeSource{agent: d}), "/agent/root-b", "localhost")
	for _, want := range []string{`claude · nosaka`, `This agent has run no background commands.`, ` – `} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, `running since`) || strings.Contains(body, `title="running"`) {
		t.Error("a stopped agent shows as running")
	}
}

func TestAgentPageTimeline(t *testing.T) {
	src := &fakeSource{agent: sampleAgentDetail(), timeline: sampleTimeline()}
	url := "/agent/root%2Fa?view=timeline&q=config+--get&from=2026-10-07T15:00:00Z&to=2026-10-07T16:00:00Z&offset=100"
	resp, body := get(t, newTestServer(t, src), url, "localhost")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d\n%s", resp.StatusCode, body)
	}
	f := src.timelineReq
	if f.Text != "config --get" || f.Offset != 100 || f.Limit != timelinePage ||
		!f.From.Equal(time.Date(2026, 10, 7, 15, 0, 0, 0, time.UTC)) || !f.To.Equal(time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)) {
		t.Errorf("filter = %+v", f)
	}
	for _, want := range []string{
		`aria-pressed="true">Timeline</a>`,
		`q=config&#43;--get`, // the toggle keeps the filter
		`<input type="search" name="q" value="config --get"`,
		`<input type="hidden" name="from" value="2026-10-07T15:00:00Z">`,
		`<ol class="tl">`,
		`<mark>config --get</mark>`, // the match is highlighted
		// every time carries its date: a bare clock time would read as today's tomorrow
		`<span class="t">Oct 7 15:39:00</span>`,
		`<span class="t">Oct 6 13:40:00</span>`,
		`250 of 1,204`,
		`101–102 of 250`,
		`Previous</a>`,
		`Next &rarr;</a>`,
		`Show all times`, // the window chip can be cleared
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestAgentPageTimelineEmptyAndPlain(t *testing.T) {
	src := &fakeSource{agent: sampleAgentDetail(), timeline: &query.Timeline{}}
	_, body := get(t, newTestServer(t, src), "/agent/root%2Fa?view=timeline&q=nothing", "localhost")
	if !strings.Contains(body, `No background command matches.`) {
		t.Error("no empty state")
	}
	src.timeline = &query.Timeline{Total: 1204, Runs: sampleTimeline().Runs}
	_, body = get(t, newTestServer(t, src), "/agent/root%2Fa?view=timeline", "localhost")
	if !strings.Contains(body, `<span class="count2">1,204</span>`) || strings.Contains(body, `Show all times`) {
		t.Errorf("an unfiltered timeline should show the total only")
	}
	if src.timelineReq.Offset != 0 || !src.timelineReq.From.IsZero() {
		t.Errorf("filter = %+v", src.timelineReq)
	}
}

func TestAgentPageErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		src  *fakeSource
		want int
	}{
		"unknown": {&fakeSource{err: api.ErrNotFound}, http.StatusNotFound},
		"nil":     {&fakeSource{}, http.StatusNotFound},
		"down":    {&fakeSource{err: api.ErrUnreachable}, http.StatusServiceUnavailable},
	} {
		resp, _ := get(t, newTestServer(t, tc.src), "/agent/x", "localhost")
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d", name, resp.StatusCode, tc.want)
		}
	}
}

func TestAgentTimelineErrors(t *testing.T) {
	// the agent is there but the timeline call fails
	for name, tc := range map[string]struct {
		err  error
		want int
	}{"not found": {api.ErrNotFound, http.StatusNotFound}, "down": {api.ErrUnreachable, http.StatusServiceUnavailable}} {
		src := &timelineErrSource{fakeSource: &fakeSource{agent: sampleAgentDetail()}, err: tc.err}
		resp, _ := get(t, newTestServer(t, src), "/agent/x?view=timeline", "localhost")
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d", name, resp.StatusCode, tc.want)
		}
	}
}

type timelineErrSource struct {
	*fakeSource
	err error
}

func (s *timelineErrSource) AgentTimeline(_ context.Context, _ string, _ query.TimelineFilter) (*query.Timeline, error) {
	return nil, s.err
}

func TestParseWhenAndLabels(t *testing.T) {
	loc := time.FixedZone("x", 2*3600)
	for _, tc := range []struct {
		in   string
		want time.Time
	}{
		{"2026-10-07T15:04:05Z", time.Date(2026, 10, 7, 15, 4, 5, 0, time.UTC)},
		{"2026-10-07T15:04", time.Date(2026, 10, 7, 15, 4, 0, 0, loc)},
		{"2026-10-07T15:04:30", time.Date(2026, 10, 7, 15, 4, 30, 0, loc)},
		{"", time.Time{}},
		{"yesterday", time.Time{}},
	} {
		if got := parseWhen(tc.in, loc); !got.Equal(tc.want) {
			t.Errorf("parseWhen(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	a, b := now.Add(-time.Hour), now.Add(-30*time.Minute)
	if got := windowLabel(a, b, now); !strings.Contains(got, " – ") {
		t.Errorf("windowLabel = %q", got)
	}
	if got := windowLabel(time.Time{}, b, now); !strings.HasPrefix(got, "until ") {
		t.Errorf("windowLabel(no from) = %q", got)
	}
	if got := windowLabel(a, time.Time{}, now); !strings.HasPrefix(got, "from ") {
		t.Errorf("windowLabel(no to) = %q", got)
	}
	if pre, m, post := splitMatch("git CONFIG --get", "config"); pre != "git " || m != "CONFIG" || post != " --get" {
		t.Errorf("splitMatch = %q %q %q", pre, m, post)
	}
	if pre, m, post := splitMatch("abc", "zzz"); pre != "abc" || m != "" || post != "" {
		t.Errorf("splitMatch miss = %q %q %q", pre, m, post)
	}
	if got := stamp(time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local), now); got != "Jan 2 03:04:05" {
		t.Errorf("stamp = %q", got)
	}
	if got := stamp(time.Date(2025, 12, 31, 23, 59, 58, 0, time.Local), now); got != "Dec 31 2025 23:59:58" {
		t.Errorf("stamp in another year = %q", got)
	}
	g := newGroupView(query.BackgroundGroup{Key: "k", Count: 2, First: now.Add(-48 * time.Hour), Last: now}, now)
	if !strings.Contains(g.T, " – ") || g.Note != "2 times" {
		t.Errorf("multi-day group = %+v", g)
	}
}

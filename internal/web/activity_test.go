package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/query"
)

func sampleActivity() *query.Activity {
	a := &query.Activity{
		HistoryBegins: now.Add(-10 * 24 * time.Hour), From: now.Add(-24 * time.Hour), To: now,
		Resource: "command", Action: "execute", EventsTotal: 2,
	}
	for _, k := range query.ActivityKinds {
		a.Rows = append(a.Rows, query.ActivityRow{Resource: k[0], Action: k[1]})
	}
	set := func(res, act string, f func(*query.ActivityRow)) {
		for i := range a.Rows {
			if a.Rows[i].Resource == res && a.Rows[i].Action == act {
				f(&a.Rows[i])
			}
		}
	}
	set("file", "read", func(r *query.ActivityRow) { r.Count, r.Outside = 214, 38 })
	set("file", "update", func(r *query.ActivityRow) { r.Count, r.Added, r.Removed = 9, 248, 61 })
	set("command", "execute", func(r *query.ActivityRow) { r.Count, r.Failed = 41, 1 })
	set("network", "connect", func(r *query.ActivityRow) { r.Count, r.Hosts = 5, "github.com:443, <evil>" })
	set("prompt", "request", func(r *query.ActivityRow) { r.Count, r.Repos = 4, 2 })
	a.Events = []query.ActivityEvent{
		{At: now.Add(-time.Hour), What: "go vet ./...", Failed: true, PromptID: 7, PromptText: "run the <tests>", Agent: "claude", Repo: "nosaka", SessionID: "404dabcd"},
		{At: now.Add(-2 * time.Hour), What: "go test", PromptID: 7, PromptText: "run the <tests>", Agent: "claude", Repo: "nosaka", SessionID: "404dabcd"},
	}
	return a
}

func TestActivityPage(t *testing.T) {
	src := &fakeSource{activity: sampleActivity()}
	s := newTestServer(t, src)
	resp, body := get(t, s, "/activity", "localhost:7357")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if src.activityReq.Days != 1 || src.activityReq.Resource != "" || src.activityReq.FailedOnly {
		t.Errorf("request = %+v", src.activityReq)
	}
	for _, want := range []string{
		`<h1>Activity <span class="hk"><span class="r">command</span><span class="sep">/</span>execute</span></h1>`,
		`<title>sentient · activity · command / execute</title>`,
		`Rolling window,`,
		// the window buttons keep the selected kind
		`<a href="/activity?action=execute&amp;resource=command" aria-pressed="true">Last 24h</a>`,
		`<a href="/activity?action=execute&amp;days=7&amp;resource=command" aria-pressed="false">7 days</a>`,
		`<span class="count">(22 kinds)</span>`,
		`<h3>file</h3>`, `<h3>launch item</h3>`, `<h3>monitor</h3>`,
		`<span class="warn">38 outside repo</span>`,
		`<span class="add">+248</span> <span class="del">&minus;61</span>`,
		`<span class="warn">1 failed</span>`,
		`github.com:443, &lt;evil&gt;`, // escaped
		`2 repos`,
		`class="tile zero"`,
		`class="tile sel" href="/activity?action=execute&amp;resource=command#events" aria-current="true"`,
		`<span class="r">command</span><span class="sep">/</span>execute`,
		`<span class="count">(2)</span>`,
		`<span>Command</span>`,
		`<span class="tag fail">failed</span>`,
		`<div class="ev failed">`,
		// both commands share a prompt and session: one header, no repeated prompt on the rows
		`<a class="gp" href="/prompt/7" title="Open this prompt">run the &lt;tests&gt;</a>`,
		`2 actions`,
		`<a href="/session/404dabcd" title="Open session">404d</a>`,
		`<span class="repo">nosaka</span>`,
		`href="/activity?action=execute&amp;failed=1&amp;resource=command#events" aria-pressed="false"`, // "failed only" chip
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	if strings.Count(body, `class="ev-group"`) != 1 || strings.Count(body, `run the &lt;tests&gt;`) != 1 {
		t.Errorf("the shared prompt should appear once: %d headers, %d mentions", strings.Count(body, `class="ev-group"`), strings.Count(body, `run the &lt;tests&gt;`))
	}
	if strings.Count(body, `<div class="grp"`) != 11 || strings.Count(body, `<a class="tile`) != 22 {
		t.Errorf("groups %d, tiles %d", strings.Count(body, `<div class="grp"`), strings.Count(body, `<a class="tile`))
	}
}

func TestActivityRequestFromTheURL(t *testing.T) {
	src := &fakeSource{activity: sampleActivity()}
	s := newTestServer(t, src)
	for path, want := range map[string]query.ActivityRequest{
		"/activity?days=7": {Days: 7},
		"/activity?days=30&resource=file&action=update":      {Days: 30, Resource: "file", Action: "update"},
		"/activity?resource=command&action=execute&failed=1": {Days: 1, Resource: "command", Action: "execute", FailedOnly: true},
		"/activity?days=99":                         {Days: 1}, // not an offered window
		"/activity?resource=file&action=explode":    {Days: 1}, // not a kind
		"/activity?resource=launch+item&action=add": {Days: 1, Resource: "launch item", Action: "add"},
	} {
		get(t, s, path, "localhost")
		if src.activityReq != want {
			t.Errorf("%s asked %+v, want %+v", path, src.activityReq, want)
		}
	}
}

func TestActivityFailedOnlyAndTruncation(t *testing.T) {
	a := sampleActivity()
	a.Events = a.Events[:1]
	a.EventsTotal = 300
	s := newTestServer(t, &fakeSource{activity: a})
	_, body := get(t, s, "/activity?resource=command&action=execute&failed=1", "localhost")
	for _, want := range []string{
		`aria-pressed="true"><span class="box">`,                      // the chip is on
		`href="/activity?action=execute&amp;resource=command#events"`, // and turns itself off
		`Showing the newest 1 of 300.`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	// no chip for a kind that has nothing failed
	b := sampleActivity()
	for i := range b.Rows {
		b.Rows[i].Failed = 0
	}
	s = newTestServer(t, &fakeSource{activity: b})
	if _, body := get(t, s, "/activity", "localhost"); strings.Contains(body, "failed only") {
		t.Error("nothing failed: no failed-only chip")
	}
	// with the filter on and nothing left, it says so and still offers the way out
	b.Events, b.EventsTotal = nil, 0
	if _, body := get(t, s, "/activity?resource=command&action=execute&failed=1", "localhost"); !strings.Contains(body, "Nothing failed in this window.") || !strings.Contains(body, "failed only") {
		t.Errorf("empty failed-only: %s", body)
	}
}

func TestActivityKindsWithOtherColumns(t *testing.T) {
	a := sampleActivity()
	a.Resource, a.Action, a.EventsTotal = "file", "update", 1
	a.Events = []query.ActivityEvent{{At: now.Add(-time.Hour), What: "/w/a.go", Added: 12, Removed: 3, Agent: "codex"}}
	s := newTestServer(t, &fakeSource{activity: a})
	_, body := get(t, s, "/activity?resource=file&action=update", "localhost")
	for _, want := range []string{`<span>Path</span>`, `<span>Result</span>`, `<span class="x"><span class="add">+12</span> <span class="del">&minus;3</span></span>`, `<span class="gp none">no prompt</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	// prompts are listed as one line each
	a.Resource, a.Action = "prompt", "request"
	a.Events = []query.ActivityEvent{{At: now, What: "first line\nsecond line", PromptID: 3, PromptText: "first line\nsecond line", SessionID: "9c1e0000", Agent: "claude"}}
	if _, body := get(t, s, "/activity?resource=prompt&action=request", "localhost"); !strings.Contains(body, `<span class="c">first line</span>`) || !strings.Contains(body, `<span>Prompt</span>`) {
		t.Errorf("prompt events: %s", body)
	}
}

func TestGroupEventsKeepsOrderAndSplitsOnPromptOrSessionChange(t *testing.T) {
	loc := now.Location()
	at := func(m int) time.Time {
		return time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, loc).Add(-time.Duration(m) * time.Minute)
	}
	ev := func(prompt, session string, m int) activityEventView {
		return activityEventView{PromptHref: prompt, SessionHref: session, What: "w", at: at(m)}
	}
	// newest first: A A B A (A again after B stays a separate run), then the same prompt in another session
	got := groupEvents([]activityEventView{ev("/p/1", "/s/a", 0), ev("/p/1", "/s/a", 3), ev("/p/2", "/s/a", 5), ev("/p/1", "/s/a", 6), ev("/p/1", "/s/b", 9)}, now)
	if len(got) != 5-1-0 && len(got) != 4 {
		t.Fatalf("%d sections, want 4", len(got))
	}
	for i, want := range []int{2, 1, 1, 1} {
		if len(got[i].Events) != want {
			t.Errorf("section %d has %d events, want %d", i, len(got[i].Events), want)
		}
	}
	if got[0].PromptHref != "/p/1" || got[1].PromptHref != "/p/2" || got[3].SessionHref != "/s/b" {
		t.Errorf("sections = %+v", got)
	}
	// the span runs oldest to newest; a single event, or events within a minute, show one time
	if !strings.HasSuffix(got[0].Span, "–"+at(0).Format("15:04")) || !strings.Contains(got[0].Span, at(3).Format("15:04")) {
		t.Errorf("span of a 3-minute run = %q", got[0].Span)
	}
	if strings.Contains(got[1].Span, "–") {
		t.Errorf("span of one event = %q", got[1].Span)
	}
	// a run across midnight shows both dates
	across := groupEvents([]activityEventView{{at: at(0).Add(24 * time.Hour)}, {at: at(0)}}, now)
	if !strings.Contains(across[0].Span, " – ") {
		t.Errorf("span across days = %q", across[0].Span)
	}
	if len(groupEvents(nil, now)) != 0 {
		t.Error("no events, no sections")
	}
}

func TestActivityCutsLongCommandsButKeepsThemInTheTitle(t *testing.T) {
	long := "git backlog add " + strings.Repeat("word ", 100)
	a := sampleActivity()
	a.Events = []query.ActivityEvent{{At: now, What: long, SessionID: "404dabcd"}, {At: now, What: "go test"}}
	s := newTestServer(t, &fakeSource{activity: a})
	_, body := get(t, s, "/activity", "localhost")
	if strings.Contains(body, long) && !strings.Contains(body, `title="`+strings.TrimSpace(long)) {
		t.Error("the full text may only appear in the title")
	}
	if !strings.Contains(body, `<span class="c" title="git backlog add word`) || !strings.Contains(body, "…</span>") {
		t.Errorf("long command not cut with a title: %s", body)
	}
	if !strings.Contains(body, `<span class="c">go test</span>`) {
		t.Error("a short command has no title")
	}
}

func TestActivitySearchBoxAndNote(t *testing.T) {
	src := &fakeSource{activity: sampleActivity()}
	s := newTestServer(t, src)
	// no search: the box is there and empty, there is no note
	_, body := get(t, s, "/activity", "localhost")
	if !strings.Contains(body, `<input type="search" name="q" value="" placeholder="Search these actions, e.g. grep"`) || strings.Contains(body, `class="ev-note"`) {
		t.Error("an empty search box and no note expected")
	}
	// the box carries the kind and window; the search is passed to the daemon
	_, body = get(t, s, "/activity?days=7&resource=command&action=execute&q=%3Cgrep%3E", "localhost")
	if src.activityReq.Search != "<grep>" || src.activityReq.Days != 7 {
		t.Errorf("request = %+v", src.activityReq)
	}
	for _, want := range []string{
		`value="&lt;grep&gt;"`,                        // escaped in the box
		`<input type="hidden" name="days" value="7">`, // the form keeps the window
		`<input type="hidden" name="resource" value="command">`,
		`Showing <b>2 of 41</b> command / execute actions containing <b>&ldquo;&lt;grep&gt;&rdquo;</b>`, // 2 matches of the kind's 41
		`>Clear</a>`,
		`href="/activity?action=execute&amp;days=7&amp;resource=command#events">Clear</a>`, // Clear drops only the search
		// the search stays when you switch window or tile
		`<a href="/activity?action=execute&amp;days=30&amp;q=%3Cgrep%3E&amp;resource=command" aria-pressed="false">30 days</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	// a search with no matches says so
	a := sampleActivity()
	a.Events, a.EventsTotal = nil, 0
	s = newTestServer(t, &fakeSource{activity: a})
	if _, body := get(t, s, "/activity?resource=command&action=execute&q=zzz", "localhost"); !strings.Contains(body, "No actions here contain &ldquo;zzz&rdquo;.") {
		t.Errorf("no match: %s", body)
	}
}

func TestActivityHidesTheResultColumnWhereItIsAlwaysEmpty(t *testing.T) {
	a := sampleActivity()
	s := newTestServer(t, &fakeSource{activity: a})
	if _, body := get(t, s, "/activity", "localhost"); !strings.Contains(body, "<span>Result</span>") || !strings.Contains(body, `class="x"`) {
		t.Error("commands keep the Result column")
	}
	a.Resource, a.Action = "file", "read"
	_, body := get(t, s, "/activity?resource=file&action=read", "localhost")
	if strings.Contains(body, "Result") || strings.Contains(body, `class="x"`) || !strings.Contains(body, `class="ev-head grouped nox"`) || !strings.Contains(body, `class="events grouped nox"`) {
		t.Error("file reads have no result to show")
	}
	for kind, want := range map[[2]string]bool{
		{"command", "execute"}: true, {"git", "commit"}: true, {"git", "force-push"}: true, {"file", "update"}: true, {"file", "create"}: true,
		{"file", "read"}: false, {"web", "fetch"}: false, {"network", "connect"}: false, {"prompt", "request"}: false, {"", ""}: false,
	} {
		if hasResult(kind[0], kind[1]) != want {
			t.Errorf("hasResult(%v) = %v", kind, !want)
		}
	}
}

func TestActivityColumnHeadings(t *testing.T) {
	for kind, want := range map[[2]string]string{
		{"web", "search"}: "Query", {"web", "fetch"}: "URL", {"file", "read"}: "Path", {"command", "execute"}: "Command",
		{"network", "connect"}: "Host", {"git", "push"}: "Command", {"monitor", "gap"}: "Gap", {"nope", "x"}: "What",
	} {
		if got := whatHead(kind[0], kind[1]); got != want {
			t.Errorf("%v: %q, want %q", kind, got, want)
		}
	}
}

func TestActivityQuietAndDown(t *testing.T) {
	a := sampleActivity()
	a.Resource, a.Action, a.Events, a.EventsTotal = "", "", nil, 0
	s := newTestServer(t, &fakeSource{activity: a})
	if _, body := get(t, s, "/activity", "localhost"); !strings.Contains(body, "Nothing happened in this window.") || strings.Contains(body, `class="ev-head"`) ||
		!strings.Contains(body, "<h1>Activity</h1>") || strings.Contains(body, `class="hk"`) {
		t.Errorf("quiet window: %s", body)
	}
	a.Resource, a.Action = "command", "execute"
	if _, body := get(t, s, "/activity", "localhost"); !strings.Contains(body, "No actions in this window.") {
		t.Errorf("selected kind without events: %s", body)
	}
	s = newTestServer(t, &fakeSource{err: api.ErrUnreachable})
	if resp, _ := get(t, s, "/activity", "localhost"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("unreachable = %d", resp.StatusCode)
	}
	// a source that returns nothing at all is treated the same way
	s = newTestServer(t, &fakeSource{})
	if resp, _ := get(t, s, "/activity", "localhost"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("empty answer = %d", resp.StatusCode)
	}
}

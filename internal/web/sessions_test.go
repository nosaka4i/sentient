package web

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/query"
)

func listedSession(id, agent, repo, first string, last time.Duration, prompts, actions, flags int64) query.SessionRow {
	return query.SessionRow{
		SessionSummary: query.SessionSummary{
			ID: id, Agent: agent, Repo: repo, Started: now.Add(last - time.Hour), LastActivity: now.Add(last),
			Prompts: prompts, Actions: actions, Flags: flags,
		},
		FirstPrompt: first,
	}
}

func sampleSessionList() *query.SessionList {
	running := listedSession("9c1e0000", "codex", "git-backlog", "add a --json flag", -10*time.Minute, 1, 18, 0)
	running.Running = true
	return &query.SessionList{
		HistoryBegins: now.Add(-10 * 24 * time.Hour),
		Total:         7,
		Agents:        []string{"claude", "codex"},
		Repos:         []string{"git-backlog", "nosaka", "rly"},
		Sessions: []query.SessionRow{
			running,
			listedSession("404dabcd", "claude", "nosaka", "run the <tests> and commit", -2*time.Hour, 3, 2140, 2),
			listedSession("a3c70000", "claude", "rly", "why does the relay drop the last line?", -5*time.Hour, 2, 21, 0),
			listedSession("89eb0000", "claude", "", "", -26*time.Hour, 3, 5, 0),
		},
	}
}

// ids returns the session ids in the order they appear on the page.
func sessionIDs(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`<a class="sid" href="/session/([^"]+)"`).FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestSessionsPage(t *testing.T) {
	src := &fakeSource{list: sampleSessionList()}
	s := newTestServer(t, src)
	resp, body := get(t, s, "/sessions", "localhost:7357")
	if resp.StatusCode != http.StatusOK || src.listDays != 7 {
		t.Fatalf("status %d, days asked %d", resp.StatusCode, src.listDays)
	}
	for _, want := range []string{
		`<h1>Sessions <span class="count">7</span></h1>`,
		`<a class="sid" href="/session/9c1e0000" title="Open session 9c1e0000">9c1e</a>`,
		`<span class="repo">git-backlog</span>`,
		`<span class="running-dot"`,
		`run the &lt;tests&gt; and commit`, // escaped
		`<td class="n num">2,140</td>`,
		`<span class="flag" title="2 attention items">2</span>`,
		`<th class="sortable" aria-sort="descending"><a class="sort" href="/sessions?sort=last%3Aasc">Last active</a></th>`,
		`<a class="chip" href="/sessions?days=0" aria-pressed="false">All time</a>`,
		`<a class="chip" href="/sessions" aria-pressed="true">7 days</a>`,
		`<b>the last 7 days</b>`,
		`4 of 4 shown`,
		`3 older sessions not in this window`,
		`<span class="nolast">no prompt text</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	// most recently active first
	if got := strings.Join(sessionIDs(body), " "); got != "9c1e0000 404dabcd a3c70000 89eb0000" {
		t.Errorf("order = %s", got)
	}
}

func TestSessionsFiltersAndSorting(t *testing.T) {
	src := &fakeSource{list: sampleSessionList()}
	s := newTestServer(t, src)
	order := func(path string) string {
		t.Helper()
		resp, body := get(t, s, path, "localhost")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d", path, resp.StatusCode)
		}
		return strings.Join(sessionIDs(body), " ")
	}
	for path, want := range map[string]string{
		"/sessions?agent=codex":                    "9c1e0000",
		"/sessions?repo=nosaka":                    "404dabcd",
		"/sessions?flagged=1":                      "404dabcd",
		"/sessions?q=RELAY":                        "a3c70000", // first prompt, any case
		"/sessions?q=git-back":                     "9c1e0000", // repo
		"/sessions?q=404d":                         "404dabcd", // session id
		"/sessions?agent=claude&flagged=1&q=tests": "404dabcd",
		"/sessions?q=nothing-like-this":            "",
		"/sessions?sort=last:asc":                  "89eb0000 a3c70000 404dabcd 9c1e0000",
		"/sessions?sort=prompts:desc":              "404dabcd 89eb0000 a3c70000 9c1e0000", // tie of 3: the later activity first
		"/sessions?sort=acts:desc":                 "404dabcd a3c70000 9c1e0000 89eb0000",
		"/sessions?sort=acts:asc":                  "89eb0000 9c1e0000 a3c70000 404dabcd",
		"/sessions?sort=flags:desc":                "404dabcd 9c1e0000 a3c70000 89eb0000",
		"/sessions?sort=sess:asc":                  "9c1e0000 404dabcd a3c70000 89eb0000", // repos A-Z, no repo last
		"/sessions?sort=sess:desc":                 "a3c70000 404dabcd 9c1e0000 89eb0000", // repos Z-A, no repo still last
		"/sessions?sort=bogus:asc":                 "9c1e0000 404dabcd a3c70000 89eb0000", // falls back to the default
		"/sessions?sort=last:sideways":             "9c1e0000 404dabcd a3c70000 89eb0000",
	} {
		if got := order(path); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}

	// the window goes to the daemon; an unknown one falls back to the default
	for path, want := range map[string]int{"/sessions?days=1": 1, "/sessions?days=30": 30, "/sessions?days=0": 0, "/sessions?days=5": 7, "/sessions?days=x": 7} {
		get(t, s, path, "localhost")
		if src.listDays != want {
			t.Errorf("%s asked for %d days, want %d", path, src.listDays, want)
		}
	}
}

func TestSessionsLinksKeepTheState(t *testing.T) {
	s := newTestServer(t, &fakeSource{list: sampleSessionList()})
	_, body := get(t, s, "/sessions?q=a&agent=claude&flagged=1&sort=acts:desc&days=30", "localhost")
	for _, want := range []string{
		// the sorted column reverses; others sort their own way first (names A-Z, numbers high first)
		`<a class="sort" href="/sessions?agent=claude&amp;days=30&amp;flagged=1&amp;q=a&amp;sort=acts%3Aasc">Actions</a>`,
		`<a class="sort" href="/sessions?agent=claude&amp;days=30&amp;flagged=1&amp;q=a&amp;sort=sess%3Aasc">Session</a>`,
		`<a class="sort" href="/sessions?agent=claude&amp;days=30&amp;flagged=1&amp;q=a&amp;sort=prompts%3Adesc">Prompts</a>`,
		// the Flagged chip turns itself off; window chips keep the filters
		`<a class="chip" href="/sessions?agent=claude&amp;days=30&amp;q=a&amp;sort=acts%3Adesc" aria-pressed="true" title="Only sessions with attention items">Flagged only</a>`,
		`<a class="chip" href="/sessions?agent=claude&amp;flagged=1&amp;q=a&amp;sort=acts%3Adesc" aria-pressed="false">7 days</a>`,
		// the form carries what its own fields don't
		`<input type="hidden" name="days" value="30">`,
		`<input type="hidden" name="flagged" value="1">`,
		`<option value="claude" selected>claude</option>`,
		`<option value="acts:desc" selected>Most actions</option>`,
		`value="a" placeholder=`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	if !strings.Contains(body, `aria-sort="descending"><a class="sort" href="/sessions?agent=claude&amp;days=30&amp;flagged=1&amp;q=a&amp;sort=acts%3Aasc">Actions`) {
		t.Error("the sorted column should say so")
	}
}

func TestSessionsEmptyStates(t *testing.T) {
	// nothing has ever happened
	s := newTestServer(t, &fakeSource{list: &query.SessionList{}})
	if _, body := get(t, s, "/sessions", "localhost"); !strings.Contains(body, "No sessions yet.") || strings.Contains(body, "<table") {
		t.Errorf("empty store: %s", body)
	}
	// sessions exist, but none in the window: offer all time
	quiet := &query.SessionList{Total: 5, Agents: []string{"claude"}}
	s = newTestServer(t, &fakeSource{list: quiet})
	_, body := get(t, s, "/sessions", "localhost")
	if !strings.Contains(body, "No sessions active in the last 7 days.") || !strings.Contains(body, `<a class="chip" href="/sessions?days=0">Show all time</a>`) {
		t.Errorf("quiet window: %s", body)
	}
	if _, all := get(t, s, "/sessions?days=0", "localhost"); strings.Contains(all, "Show all time") {
		t.Error("all time has nothing wider to offer")
	}
	// filters that match nothing: offer to clear them
	s = newTestServer(t, &fakeSource{list: sampleSessionList()})
	_, body = get(t, s, "/sessions?q=zzz&agent=codex", "localhost")
	if !strings.Contains(body, "No sessions match these filters.") || !strings.Contains(body, `<a class="chip" href="/sessions">Clear filters</a>`) {
		t.Errorf("no match: %s", body)
	}
}

func TestSessionsDownAndNoOlderNote(t *testing.T) {
	s := newTestServer(t, &fakeSource{err: api.ErrUnreachable})
	if resp, body := get(t, s, "/sessions", "localhost"); resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "sentientd") {
		t.Errorf("unreachable = %d", resp.StatusCode)
	}
	s = newTestServer(t, &fakeSource{err: errors.New("boom")})
	if resp, body := get(t, s, "/sessions", "localhost"); resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "boom") {
		t.Errorf("error = %d", resp.StatusCode)
	}
	// the whole history in the window: no "older sessions" note
	l := sampleSessionList()
	l.Total = 4
	s = newTestServer(t, &fakeSource{list: l})
	if _, body := get(t, s, "/sessions", "localhost"); strings.Contains(body, "older session") {
		t.Error("nothing is older than this window")
	}
	l.Total = 5
	if _, body := get(t, s, "/sessions", "localhost"); !strings.Contains(body, "1 older session not in this window") {
		t.Error("singular note missing")
	}
}

func TestHeaderNavMarksTheCurrentPage(t *testing.T) {
	s := newTestServer(t, &fakeSource{st: fullStatus(), list: sampleSessionList(), window: sampleWindow(), health: healthReport()})
	for path, current := range map[string]string{"/": "Dashboard", "/sessions": "Sessions", "/prompts": "Prompts", "/health": ""} {
		_, body := get(t, s, path, "localhost")
		for _, link := range []string{`<a class="icon-btn" href="/"`, `<a class="ic" href="/sessions"`, `<a class="ic" href="/prompts"`, `<a class="brand" href="/"`} {
			if !strings.Contains(body, link) {
				t.Errorf("%s: header has no %s link", path, link)
			}
		}
		// the home icon carries its name in aria-label; the other pages are text links
		marked := regexp.MustCompile(`<a (?:class="(?:icon-btn|ic)" )?href="/[a-z]*" aria-current="page"(?: aria-label="Dashboard")?[^>]*>(?:<svg.*?</svg>)?\s*([A-Za-z]*)`).FindStringSubmatch(body)
		home := strings.Contains(body, `<a class="icon-btn" href="/" aria-current="page" aria-label="Dashboard"`)
		switch {
		case current == "Dashboard" && !home, current != "Dashboard" && home:
			t.Errorf("%s: home icon marked current = %v, want %v", path, home, current == "Dashboard")
		case current != "" && current != "Dashboard" && (marked == nil || marked[1] != current):
			t.Errorf("%s: current page marked as %v, want %q", path, marked, current)
		}
	}
}

func TestThousands(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 7: "7", 999: "999", 1000: "1,000", 2140: "2,140", 1234567: "1,234,567", -1234: "-1,234"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q, want %q", n, got, want)
		}
	}
}

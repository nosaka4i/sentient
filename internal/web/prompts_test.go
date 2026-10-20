package web

import (
	"encoding/json"
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

func sampleWindow() *query.PromptWindow {
	other := promptRow(4, "other session\nwith a second line", now.Add(-3*time.Hour), "codex", "sentient")
	other.SessionID = "9c1e0000"
	other.Running = true
	first := promptRow(5, "make it <fast>", now.Add(-time.Hour), "claude", "nosaka")
	first.Flags = 1
	long := promptRow(6, "  "+strings.Repeat("word ", 100)+"\n\nthen "+strings.Repeat("more ", 400), now.Add(-5*time.Hour), "claude", "nosaka")
	return &query.PromptWindow{
		HistoryBegins: now.Add(-72 * time.Hour),
		Prompts:       []query.PromptRow{first, other, long},
		Sessions: []query.SessionSummary{
			{ID: "404dabcd", Agent: "claude", Repo: "nosaka", Started: now.Add(-6 * time.Hour), LastActivity: now.Add(-time.Hour), Prompts: 12, Actions: 80, Flags: 1},
			{ID: "9c1e0000", Agent: "codex", Repo: "sentient", Started: now.Add(-3 * time.Hour), LastActivity: now.Add(-2 * time.Hour), Prompts: 1, Actions: 3},
		},
		Matching: 9, More: true, Oldest: now.Add(-5 * time.Hour), Widened: true,
		Agents: []string{"claude", "codex"}, Repos: []string{"nosaka", "sentient"},
		Menu:          []query.SessionOption{{ID: "404dabcd", Agent: "claude", Repo: "nosaka"}},
		OlderSessions: 4,
	}
}

func TestPromptsPageEmbedsTheFirstWindow(t *testing.T) {
	src := &fakeSource{window: sampleWindow()}
	s := newTestServer(t, src)
	resp, body := get(t, s, "/prompts", "localhost:7357")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d\n%s", resp.StatusCode, body)
	}
	if r := src.windowReq; r.Days != DefaultWindowDays || r.SessionID != "" || !r.Before.IsZero() {
		t.Errorf("request = %+v, want the default window", r)
	}
	for _, want := range []string{
		`<title>sentient · prompts</title>`,
		`<a class="back" href="/">&larr; Dashboard</a>`,
		`<div id="view" aria-live="polite">`,
		`id="f-session"`, `id="f-sort"`, `id="f-flag"`, `data-days="30"`,
		`<script id="prompts-data" type="application/json">`,
		`<script src="/static/prompts.js"></script>`,
		`Monitored from`, // the header still has the history start
		`<noscript>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}

	// The embedded JSON is the window, and can't end the script early.
	i := strings.Index(body, `<script id="prompts-data" type="application/json">`)
	j := strings.Index(body[i:], `</script>`)
	raw := body[i+len(`<script id="prompts-data" type="application/json">`) : i+j]
	var w windowJSON
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatalf("embedded JSON: %v\n%s", err, raw)
	}
	if w.Days != DefaultWindowDays || !w.Widened || !w.More || w.Matching != 9 || len(w.Prompts) != 3 || len(w.Sessions) != 2 || w.Older != 4 {
		t.Errorf("window = %+v", w)
	}
	if strings.Contains(raw, "<fast>") {
		t.Error("prompt text must be escaped inside the script")
	}
	if t.Failed() {
		t.Log(body)
	}

	for _, tc := range []struct {
		q    string
		want int
	}{{"?days=7", 7}, {"?days=0", 0}, {"?days=-1", DefaultWindowDays}, {"?days=x", DefaultWindowDays}, {"?days=99999", DefaultWindowDays}} {
		get(t, s, "/prompts"+tc.q, "localhost")
		if src.windowReq.Days != tc.want {
			t.Errorf("%s: asked for %d days, want %d", tc.q, src.windowReq.Days, tc.want)
		}
	}

	down := newTestServer(t, &fakeSource{err: api.ErrUnreachable})
	if resp, _ := get(t, down, "/prompts", "localhost"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("unreachable: %d", resp.StatusCode)
	}
}

func TestPromptsDataIsCompactJSON(t *testing.T) {
	src := &fakeSource{window: sampleWindow()}
	s := newTestServer(t, src)
	resp, body := get(t, s, "/prompts/data?days=30&before=2026-10-07T10:00:00Z", "localhost")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d type %q cache %q", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control"))
	}
	if r := src.windowReq; r.Days != 30 || !r.Before.Equal(time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("request = %+v", r)
	}
	var w windowJSON
	if err := json.Unmarshal([]byte(body), &w); err != nil {
		t.Fatal(err)
	}
	if w.Now != now.UnixMilli() || w.Oldest != now.Add(-5*time.Hour).UnixMilli() || len(w.Menu) != 1 || w.Menu[0].Repo != "nosaka" {
		t.Errorf("window = %+v", w)
	}
	// Each prompt names its session by index; times are Unix ms.
	p0, p1, p2 := w.Prompts[0], w.Prompts[1], w.Prompts[2]
	if p0.ID != 5 || p0.S != 0 || p0.T != now.Add(-time.Hour).UnixMilli() || p0.Text != "make it <fast>" || p0.Dur != 300 || p0.Actions != 3 || p0.Flags != 1 || p0.Body != "" {
		t.Errorf("first prompt = %+v", p0)
	}
	if p1.S != 1 || !p1.Running || p1.Text != "other session" || p1.Body != "other session with a second line" {
		t.Errorf("second prompt = %+v", p1)
	}
	// A long prompt carries a shortened first line and a bounded body for search.
	if n := len([]rune(p2.Text)); n > textWidth || !strings.HasSuffix(p2.Text, "…") || len([]rune(p2.Body)) > bodyWidth || !strings.Contains(p2.Body, "then more") {
		t.Errorf("long prompt: text %d runes, body %d runes", n, len([]rune(p2.Body)))
	}
	if w.Sessions[0].ID != "404dabcd" || w.Sessions[0].Prompts != 12 || w.Sessions[0].Started != now.Add(-6*time.Hour).UnixMilli() {
		t.Errorf("sessions = %+v", w.Sessions)
	}

	// One session, and the error cases.
	get(t, s, "/prompts/data?session=404dabcd", "localhost")
	if src.windowReq.SessionID != "404dabcd" {
		t.Errorf("session request = %+v", src.windowReq)
	}
	if resp, _ := get(t, s, "/prompts/data?before=yesterday", "localhost"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad before: %d", resp.StatusCode)
	}
	for _, tc := range []struct {
		err  error
		want int
	}{{api.ErrUnreachable, http.StatusServiceUnavailable}, {api.ErrNotFound, http.StatusInternalServerError}} {
		if resp, _ := get(t, newTestServer(t, &fakeSource{err: tc.err}), "/prompts/data", "localhost"); resp.StatusCode != tc.want {
			t.Errorf("%v: status %d, want %d", tc.err, resp.StatusCode, tc.want)
		}
	}
}

func TestEmptyWindowIsValidJSON(t *testing.T) {
	// No prompts, no sessions, no agents: empty arrays, never null, so the script needs no checks.
	s := newTestServer(t, &fakeSource{window: &query.PromptWindow{}})
	_, body := get(t, s, "/prompts/data", "localhost")
	for _, want := range []string{`"prompts":[]`, `"sessions":[]`, `"agents":[]`, `"repos":[]`, `"menu":[]`} {
		if !strings.Contains(body, want) {
			t.Errorf("empty window missing %s: %s", want, body)
		}
	}
}

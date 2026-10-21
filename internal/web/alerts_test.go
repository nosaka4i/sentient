package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/query"
)

func alertRow(id int64, sev, status string, at time.Time) query.AlertRow {
	return query.AlertRow{
		ID: id, At: at, Severity: sev, Rule: "test-rule", Status: status, ActionID: 50 + id, Resource: "file", Action: "update",
		Path: "/home/u/.zshrc", PromptID: 7, PromptText: "make the <build> use staging\nplease", Agent: "claude", Repo: "nosaka", SessionID: "404dabcd",
	}
}

func sampleAlerts() *query.AlertList {
	gap := query.AlertRow{ID: 4, At: now.Add(-30 * time.Hour), Severity: "medium", Rule: "monitor-gap", Status: query.AlertReviewed, ReviewedAt: now.Add(-20 * time.Hour)}
	rev := alertRow(3, "high", query.AlertReviewed, now.Add(-26*time.Hour))
	rev.ReviewedAt = now.Add(-25 * time.Hour)
	return &query.AlertList{
		HistoryBegins: now.Add(-10 * 24 * time.Hour),
		Counts:        query.AlertCounts{Open: 7, Reviewed: 2, OpenHigh: 1, OpenMedium: 4, OpenLow: 2},
		Alerts: []query.AlertRow{
			alertRow(1, "high", query.AlertOpen, now.Add(-time.Hour)),
			alertRow(2, "low", query.AlertOpen, now.Add(-2*time.Hour)),
			rev,
			gap,
		},
	}
}

func TestAlertsPage(t *testing.T) {
	src := &fakeSource{alerts: sampleAlerts()}
	s := newTestServer(t, src)
	s.home = "/home/u" // so the alerts' /home/u/.zshrc shows as ~/.zshrc
	resp, body := get(t, s, "/alerts", "localhost")
	if resp.StatusCode != http.StatusOK || src.alertFilter != (query.AlertFilter{Status: query.AlertOpen}) {
		t.Fatalf("status %d, filter %+v", resp.StatusCode, src.alertFilter)
	}
	for _, want := range []string{
		`<h1>Alerts <span class="count n-open">7</span></h1>`,
		`<a class="chip" href="/alerts" aria-pressed="true">Open</a>`,
		`<a class="chip" href="/alerts?status=reviewed" aria-pressed="false">Reviewed</a>`,
		`<a class="chip" href="/alerts?sev=high" aria-pressed="false">High</a>`,
		`<h2 class="dayh">Today<span class="d">Oct 7</span></h2>`,
		`<h2 class="dayh">Yesterday<span class="d">Oct 6</span></h2>`, // the 26h-old alert
		`<div class="alert high">`,
		`<div class="alert low">`,
		`<div class="alert high reviewed">`,
		`<span class="sev sev-high">High</span>`,
		`<span class="path">~/.zshrc</span>`,
		`<a class="hit" href="/alert/1" aria-label="Open alert: ~/.zshrc"></a>`,
		`<span class="r">file</span><span class="sep">/</span>update`,
		`<a class="plink" href="/prompt/7">make the &lt;build&gt; use staging</a>`, // escaped, first line only
		`claude · nosaka`,
		`reviewed Oct 6`,                        // the reviewed one says when
		`<span class="path">monitor gap</span>`, // no action: the rule's name in words
		`no agent`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	// the heading carries the open count; there is no subtitle repeating it
	if strings.Contains(body, `class="sub"`) || strings.Contains(body, "history from") {
		t.Error("the alerts heading has no subtitle")
	}
	// the gap alert has no action and so no resource/action or prompt link
	if strings.Count(body, `<a class="plink"`) != 3 {
		t.Errorf("%d prompt links, want 3 (the gap has none)", strings.Count(body, `<a class="plink"`))
	}
}

func TestAlertsFiltersAndEmptyStates(t *testing.T) {
	src := &fakeSource{alerts: sampleAlerts()}
	s := newTestServer(t, src)
	for path, want := range map[string]query.AlertFilter{
		"/alerts?status=reviewed":        {Status: query.AlertReviewed},
		"/alerts?status=all":             {},
		"/alerts?sev=high":               {Status: query.AlertOpen, Severity: "high"},
		"/alerts?status=all&sev=low":     {Severity: "low"},
		"/alerts?status=bogus&sev=bogus": {Status: query.AlertOpen}, // unknown values fall back
	} {
		get(t, s, path, "localhost")
		if src.alertFilter != want {
			t.Errorf("%s asked %+v, want %+v", path, src.alertFilter, want)
		}
	}
	// chips keep the other filter
	_, body := get(t, s, "/alerts?status=reviewed&sev=medium", "localhost")
	for _, want := range []string{
		`<a class="chip" href="/alerts?sev=medium" aria-pressed="false">Open</a>`,
		`<a class="chip" href="/alerts?sev=medium&amp;status=reviewed" aria-pressed="true">Reviewed</a>`,
		`<a class="chip" href="/alerts?status=reviewed" aria-pressed="false">All</a>`, // severity All drops sev
		`<a class="chip" href="/alerts?sev=medium&amp;status=reviewed" aria-pressed="true">Medium</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	// nothing at all: the all-clear
	s = newTestServer(t, &fakeSource{alerts: &query.AlertList{}})
	if _, body := get(t, s, "/alerts", "localhost"); !strings.Contains(body, "Nothing needs your attention") || strings.Contains(body, `class="dayh"`) {
		t.Errorf("empty list: %s", body)
	}
	// a filter that matches nothing offers the way back
	if _, body := get(t, s, "/alerts?sev=high", "localhost"); !strings.Contains(body, "No alerts match these filters.") || !strings.Contains(body, `href="/alerts">Show open alerts</a>`) {
		t.Errorf("empty filtered list: %s", body)
	}
	// down
	s = newTestServer(t, &fakeSource{err: api.ErrUnreachable})
	if resp, _ := get(t, s, "/alerts", "localhost"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("unreachable = %d", resp.StatusCode)
	}
	if resp, _ := get(t, newTestServer(t, &fakeSource{}), "/alerts", "localhost"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("empty answer = %d", resp.StatusCode)
	}
}

// alertDetailFixture is a file-update alert whose prompt has a trace and a diff.
func alertDetailFixture() (*query.AlertDetail, *query.PromptDetail) {
	pd := sampleDetail()
	pd.Steps = nil
	for i := 1; i <= 7; i++ {
		pd.Steps = append(pd.Steps, query.Step{N: int64(i), Resource: "command", Action: "execute", Path: "step" + string(rune('0'+i)), Evidence: "log", ActionID: int64(100 + i)})
	}
	pd.Steps[3] = query.Step{N: 4, Resource: "file", Action: "update", Path: "/w/nosaka/ios/Config.plist", Evidence: "log", ActionID: 77}
	a := alertRow(9, "high", query.AlertOpen, now.Add(-time.Hour))
	a.ActionID, a.Path, a.PromptID, a.PromptText = 77, "/w/nosaka/ios/Config.plist", 7, "make the build use staging"
	return &query.AlertDetail{HistoryBegins: now.Add(-48 * time.Hour), Alert: a, RawRecord: `{"type":"assistant","x":"<b>"}`}, pd
}

func TestAlertDetailPage(t *testing.T) {
	d, pd := alertDetailFixture()
	src := &fakeSource{alert: d, detail: pd}
	s := newTestServer(t, src)
	resp, body := get(t, s, "/alert/9", "localhost")
	if resp.StatusCode != http.StatusOK || src.alertID != 9 || src.asked != 7 {
		t.Fatalf("status %d, asked alert %d prompt %d", resp.StatusCode, src.alertID, src.asked)
	}
	for _, want := range []string{
		`<a class="back" href="/alerts">`,
		`<span class="sev sev-high">High</span>`,
		`<h1>/w/nosaka/ios/Config.plist</h1>`,
		`<a class="sess" href="/session/404dabcd"`,
		`<a class="plink" href="/prompt/7"`,
		`rule <span class="mono">test-rule</span>`,
		`<span class="state-open"><span class="dot"></span>Open</span>`,
		`<form method="post" action="/alert/9/review">`,
		`<input type="hidden" name="reviewed" value="1">`,
		`>Mark reviewed</button>`,
		`Revert this change <span class="soon">soon</span>`,
		`<a class="btn" href="/prompt/7">Open prompt</a>`,
		// the diff of the file this alert is about
		`id="h-what">What changed`,
		`<span class="d">-&lt;old&gt;</span>`,
		// the trace: two steps either side of step 4, the flagged one marked, and what was left out
		`&hellip; 1 earlier step</li>`,
		`<li class="step effect flagged">`,
		`<span class="tag hi">flagged</span>`,
		`&hellip; 1 more step</li>`,
		// evidence, escaped
		`The agent's own record of this action.`,
		`{&#34;type&#34;:&#34;assistant&#34;,&#34;x&#34;:&#34;&lt;b&gt;&#34;}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	if strings.Count(body, `<li class="step`) != 5 {
		t.Errorf("%d steps shown, want 5", strings.Count(body, `<li class="step`))
	}
}

func TestAlertDetailReviewedAndBare(t *testing.T) {
	d, pd := alertDetailFixture()
	d.Alert.Status, d.Alert.ReviewedAt = query.AlertReviewed, now.Add(-time.Minute)
	s := newTestServer(t, &fakeSource{alert: d, detail: pd})
	_, body := get(t, s, "/alert/9", "localhost")
	for _, want := range []string{`<span class="state-done">`, `Reviewed · reviewed `, `<input type="hidden" name="reviewed" value="0">`, `>Reopen</button>`} {
		if !strings.Contains(body, want) {
			t.Errorf("reviewed page is missing %q", want)
		}
	}
	if strings.Contains(body, "btn primary") {
		t.Error("a reviewed alert has no primary button")
	}

	// an alert with no action or prompt (a monitor gap) still renders, with no trace, diff or evidence
	gap := &query.AlertDetail{Alert: query.AlertRow{ID: 4, At: now.Add(-time.Hour), Severity: "medium", Rule: "monitor-gap", Status: query.AlertOpen}}
	s = newTestServer(t, &fakeSource{alert: gap})
	resp, body := get(t, s, "/alert/4", "localhost")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "<h1>monitor gap</h1>") {
		t.Fatalf("gap alert = %d", resp.StatusCode)
	}
	for _, not := range []string{"What changed", "Where it happened", "Evidence", "Open prompt", "Revert this change"} {
		if strings.Contains(body, not) {
			t.Errorf("a gap alert shouldn't show %q", not)
		}
	}

	// the prompt can't be loaded: the alert page still works, without its trace
	d2, _ := alertDetailFixture()
	s = newTestServer(t, &fakeSource{alert: d2, err: nil, detail: nil})
	if resp, body := get(t, s, "/alert/9", "localhost"); resp.StatusCode != http.StatusOK || strings.Contains(body, "Where it happened") {
		t.Errorf("no prompt detail: %d", resp.StatusCode)
	}
}

func TestAlertDetailErrors(t *testing.T) {
	d, _ := alertDetailFixture()
	s := newTestServer(t, &fakeSource{alert: d})
	for path, want := range map[string]int{"/alert/abc": 404, "/alert/-": 404} {
		if resp, _ := get(t, s, path, "localhost"); resp.StatusCode != want {
			t.Errorf("%s = %d, want %d", path, resp.StatusCode, want)
		}
	}
	s = newTestServer(t, &fakeSource{err: api.ErrNotFound})
	if resp, _ := get(t, s, "/alert/9", "localhost"); resp.StatusCode != 404 {
		t.Errorf("unknown alert = %d", resp.StatusCode)
	}
	s = newTestServer(t, &fakeSource{})
	if resp, _ := get(t, s, "/alert/9", "localhost"); resp.StatusCode != 404 {
		t.Errorf("no alert returned = %d", resp.StatusCode)
	}
	s = newTestServer(t, &fakeSource{err: api.ErrUnreachable})
	if resp, _ := get(t, s, "/alert/9", "localhost"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("unreachable = %d", resp.StatusCode)
	}
}

func post(t *testing.T, s *Server, path string, header map[string]string, form url.Values) *http.Response {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Host = "localhost:7357"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result()
}

func TestReviewingAnAlert(t *testing.T) {
	src := &fakeSource{}
	s := newTestServer(t, src)
	own := map[string]string{"Origin": "http://localhost:7357"}

	resp := post(t, s, "/alert/9/review", own, url.Values{"reviewed": {"1"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/alert/9" || !src.reviewed || src.reviewedID != 9 || src.reviewCalls != 1 {
		t.Errorf("review: %d -> %q, reviewed=%v id=%d calls=%d", resp.StatusCode, resp.Header.Get("Location"), src.reviewed, src.reviewedID, src.reviewCalls)
	}
	if resp = post(t, s, "/alert/9/review", own, url.Values{"reviewed": {"0"}}); resp.StatusCode != http.StatusSeeOther || src.reviewed {
		t.Errorf("reopen: %d reviewed=%v", resp.StatusCode, src.reviewed)
	}
	// the browser's Sec-Fetch-Site also counts as our own page
	if resp = post(t, s, "/alert/9/review", map[string]string{"Sec-Fetch-Site": "same-origin"}, url.Values{"reviewed": {"1"}}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("same-origin fetch = %d", resp.StatusCode)
	}

	// anything that isn't our own page is refused, and changes nothing
	calls := src.reviewCalls
	for name, h := range map[string]map[string]string{
		"another site":     {"Origin": "https://evil.example"},
		"another port":     {"Origin": "http://localhost:9999"},
		"cross-site fetch": {"Sec-Fetch-Site": "cross-site"},
		"no origin at all": {},
		"garbage origin":   {"Origin": "http://[::1"},
	} {
		if resp := post(t, s, "/alert/9/review", h, url.Values{"reviewed": {"1"}}); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", name, resp.StatusCode)
		}
	}
	if src.reviewCalls != calls {
		t.Error("a refused request changed an alert")
	}

	// a plain GET to the review address is not allowed
	if r, _ := get(t, s, "/alert/9/review", "localhost"); r.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET review = %d", r.StatusCode)
	}
	if resp = post(t, s, "/alert/xyz/review", own, nil); resp.StatusCode != 404 {
		t.Errorf("bad id = %d", resp.StatusCode)
	}
	// unknown alert, and sentientd down
	s = newTestServer(t, &fakeSource{reviewErr: api.ErrNotFound})
	if resp = post(t, s, "/alert/9/review", own, nil); resp.StatusCode != 404 {
		t.Errorf("unknown alert = %d", resp.StatusCode)
	}
	s = newTestServer(t, &fakeSource{reviewErr: errors.New("boom")})
	if resp = post(t, s, "/alert/9/review", own, nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("error = %d", resp.StatusCode)
	}
	// a body that can't be parsed
	req := httptest.NewRequest("POST", "/alert/9/review", strings.NewReader("%zz"))
	req.Host = "localhost"
	req.Header.Set("Origin", "http://localhost")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newTestServer(t, &fakeSource{}).Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad form = %d", rec.Code)
	}
}

func TestDashboardNeedsAttention(t *testing.T) {
	al := sampleAlerts()
	al.Alerts = al.Alerts[:2] // the two open ones listed; 7 open in all
	src := &fakeSource{st: fullStatus(), alerts: al}
	s := newTestServer(t, src)
	s.home = "/home/u"
	_, body := get(t, s, "/", "localhost")
	if src.alertFilter != (query.AlertFilter{Status: query.AlertOpen, Limit: attentionRows}) {
		t.Errorf("filter = %+v", src.alertFilter)
	}
	for _, want := range []string{
		`Needs attention <span class="count">(7)</span>`,
		`aria-label="1 high, 4 medium, 2 low"`,
		`<span class="h" style="flex-grow:1"></span>`,
		`<span class="m" style="flex-grow:4"></span>`,
		`<a class="alert high" href="/alert/1" title="Open details">`,
		`<span class="path">~/.zshrc</span>`,
		`5 more open`,
		`href="/alerts">See all</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard is missing %q", want)
		}
	}
	if strings.Contains(body, "Nothing needs your attention") {
		t.Error("with alerts open there is no all-clear")
	}
	// none open: the all-clear and no severity bar
	s = newTestServer(t, &fakeSource{st: fullStatus(), alerts: &query.AlertList{}})
	if _, body := get(t, s, "/", "localhost"); !strings.Contains(body, "Nothing needs your attention") || strings.Contains(body, `class="sevbar"`) {
		t.Error("expected the all-clear and no severity bar")
	}
	// the alert call failing is the same as sentientd being down
	s = newTestServer(t, &fakeSource{st: fullStatus(), alerts: al, err: nil})
	if resp, _ := get(t, s, "/", "localhost"); resp.StatusCode != 200 {
		t.Errorf("dashboard = %d", resp.StatusCode)
	}
}

func TestDayLabels(t *testing.T) {
	cases := []struct {
		at          time.Time
		label, date string
	}{
		{now.Add(-time.Hour), "Today", "Oct 7"},
		{now.Add(-26 * time.Hour), "Yesterday", "Oct 6"},
		{now.Add(-72 * time.Hour), "Sunday", "Oct 4"},
		{time.Date(2025, 12, 30, 9, 0, 0, 0, now.Location()), "Tuesday", "Dec 30 2025"},
	}
	for _, c := range cases {
		if l, d := dayLabel(c.at, now); l != c.label || d != c.date {
			t.Errorf("dayLabel(%v) = %q %q, want %q %q", c.at, l, d, c.label, c.date)
		}
	}
}

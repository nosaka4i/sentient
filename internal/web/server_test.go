package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/health"
	"github.com/nosaka4i/sentient/internal/query"
	"github.com/nosaka4i/sentient/internal/version"
)

var now = time.Date(2026, 10, 7, 15, 40, 0, 0, time.Local)

type fakeSource struct {
	st          *query.Status
	health      *health.Report
	detail      *query.PromptDetail
	window      *query.PromptWindow
	session     *query.SessionDetail
	list        *query.SessionList
	listDays    int // the days the sessions list was last asked for
	activity    *query.Activity
	activityReq query.ActivityRequest // the activity request last made
	err         error
	from, to    time.Time
	asked       int64 // the prompt id last requested
	// the window request last made, and the session id last requested
	windowReq    query.WindowRequest
	askedSession string
}

func (f *fakeSource) PromptDetail(ctx context.Context, id int64) (*query.PromptDetail, error) {
	f.asked = id
	return f.detail, f.err
}

func (f *fakeSource) PromptWindow(ctx context.Context, req query.WindowRequest) (*query.PromptWindow, error) {
	f.windowReq = req
	return f.window, f.err
}

func (f *fakeSource) SessionDetail(ctx context.Context, id string) (*query.SessionDetail, error) {
	f.askedSession = id
	return f.session, f.err
}

func (f *fakeSource) SessionList(ctx context.Context, days int) (*query.SessionList, error) {
	f.listDays = days
	return f.list, f.err
}

func (f *fakeSource) ActivityList(ctx context.Context, req query.ActivityRequest) (*query.Activity, error) {
	f.activityReq = req
	return f.activity, f.err
}

func (f *fakeSource) Status(ctx context.Context, from, to time.Time) (*query.Status, string, error) {
	f.from, f.to = from, to
	return f.st, "test", f.err
}

func (f *fakeSource) Health(ctx context.Context) (*health.Report, error) {
	return f.health, f.err
}

func newTestServer(t *testing.T, src Source) *Server {
	t.Helper()
	s, err := New(src, "/run/sentient/sentientd.sock", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	return s
}

func get(t *testing.T, s *Server, path, host string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result(), rec.Body.String()
}

func fullStatus() *query.Status {
	rows := make([]query.ActivityRow, 0, len(query.StatusRows))
	for _, r := range query.StatusRows {
		rows = append(rows, query.ActivityRow{Resource: r[0], Action: r[1]})
	}
	rows[0].Count, rows[0].Repos = 4, 2
	rows[1].Count, rows[1].Outside = 214, 38
	rows[3].Count, rows[3].Added, rows[3].Removed = 9, 248, 61
	rows[5].Count, rows[5].Failed = 41, 2
	rows[9].Count, rows[9].Hosts = 3, "a.dev, <b>.dev"
	return &query.Status{
		HistoryBegins: now.Add(-(30*time.Hour + 28*time.Minute)),
		Activity:      rows,
		PromptsTotal:  6,
		Prompts: []query.PromptRow{
			{ID: 4, Text: "add a --json flag to list", Started: now.Add(-10 * time.Minute), Duration: 12 * time.Minute,
				Running: true, Agent: "codex", Repo: "git-backlog", SessionID: "9c1e77", Actions: 18},
			{ID: 1, Text: "<pasted_content>\na\nb\n</pasted_content>\nthis <crash> log", Started: now.Add(-time.Hour),
				Duration: 36 * time.Minute, Agent: "claude", Repo: "nosaka", SessionID: "404d12", Actions: 112, Flags: 2},
			{ID: 7, Text: "other agent", Started: now.Add(-2 * time.Hour), Agent: "gemini", Repo: "x", SessionID: "s", Flags: 1},
		},
	}
}

func TestDashboard(t *testing.T) {
	src := &fakeSource{st: fullStatus()}
	resp, body := get(t, newTestServer(t, src), "/", "127.0.0.1:7357")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d\n%s", resp.StatusCode, body)
	}
	if src.to.Sub(src.from) != 24*time.Hour || !src.to.Equal(now) {
		t.Errorf("window = %v..%v, want the last 24h", src.from, src.to)
	}
	for _, want := range []string{
		`<meta http-equiv="refresh" content="30">`,
		`History from <time datetime="` + now.Add(-(30*time.Hour + 28*time.Minute)).Format("2006-01-02T15:04") + `">Oct 6 09:12</time> (1d 6h ago)`,
		`Needs attention <span class="count">(0)</span>`,
		`Nothing needs your attention`,
		`href="/alerts"`,
		`<a class="tile" href="/activity?action=request&amp;resource=prompt">`,
		`<span class="num">214</span>`,
		`<span class="warn">38 outside repo</span>`,
		`<span class="sum mono"><span class="add">+248</span> <span class="del">&minus;61</span></span>`,
		// command execute: the tile's main link and a separate "failed" link
		`<div class="tile linked">`,
		`<a class="stretch" href="/activity?action=execute&amp;resource=command">`,
		`<a class="sublink warn" href="/activity?action=execute&amp;resource=command&amp;failed=1">2 failed</a>`,
		`<a class="icon-btn" href="/health" aria-label="Health"`,
		`a.dev, &lt;b&gt;.dev`, // escaped
		`2 repos`,
		`<a class="tile wide" href="/activity?action=connect&amp;resource=network">`,
		`Prompts <span class="count">(6)</span>`,
		`href="/prompts"`,
		`<tr data-href="/prompt/4">`,
		`<span class="running-dot"`,
		`<span class="running-label">running</span>`,
		`<use href="#i-codex"/>`,
		`<a href="/session/9c1e77">9c1e</a>`,
		`<td class="dur">12m&#43;</td>`, // html/template escapes "+"
		`title="18 actions so far"`,
		`<span class="pasted">pasted 2 lines</span> this &lt;crash&gt; log`,
		`<span class="flag" title="2 attention items">2</span>`,
		`<span class="flag" title="1 attention item">1</span>`,
		`<td class="agent">gemini</td>`, // no icon: the name
		`v` + version.Version + `</span>`,
		`Copyright &copy; 2026 <a href="https://nosaka.ai">nosaka.ai</a>`,
		`MIT License`,
		`<script src="/static/sentient.js"></script>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	if strings.Contains(body, `class="sevbar"`) {
		t.Error("severity bar shown with 0 alerts")
	}
	if n := strings.Count(body, `<a class="tile`) + strings.Count(body, `<div class="tile `); n != 11 {
		t.Errorf("tiles = %d, want 11", n)
	}
	if resp.Header.Get("X-Frame-Options") != "DENY" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v", resp.Header)
	}
}

func TestDashboardFreshInstall(t *testing.T) {
	st := &query.Status{}
	for _, r := range query.StatusRows {
		st.Activity = append(st.Activity, query.ActivityRow{Resource: r[0], Action: r[1]})
	}
	_, body := get(t, newTestServer(t, &fakeSource{st: st}), "/", "localhost:7357")
	for _, want := range []string{
		`Prompts <span class="count">(0)</span>`,
		`<div class="table-scroll" style="display:none">`,
		`No prompts yet. They show up here as soon as an agent starts working.`,
		`<span class="num">0</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("fresh dashboard missing %q", want)
		}
	}
	if strings.Contains(body, "sublink") {
		t.Error("failed link shown with 0 failures")
	}
	if strings.Contains(body, `class="since"`) {
		t.Error("History from shown with no history")
	}
}

func TestSentientdDown(t *testing.T) {
	resp, body := get(t, newTestServer(t, &fakeSource{err: api.ErrUnreachable}), "/", "[::1]:7357")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
	for _, want := range []string{"sentientd is not reachable", "/run/sentient/sentientd.sock", "systemctl status sentientd", `content="30"`} {
		if !strings.Contains(body, want) {
			t.Errorf("down page missing %q", want)
		}
	}
	if strings.Contains(body, "Error:") {
		t.Error("plain unreachability should not show an error detail")
	}

	_, body = get(t, newTestServer(t, &fakeSource{err: errors.New("rpc exploded")}), "/", "127.0.0.1")
	if !strings.Contains(body, "rpc exploded") {
		t.Error("other errors should show their detail")
	}
}

func TestStaticAndRouting(t *testing.T) {
	s := newTestServer(t, &fakeSource{st: fullStatus()})
	resp, body := get(t, s, "/static/sentient.css", "127.0.0.1:7357")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css") || !strings.Contains(body, "--high: #04d9ff") {
		t.Errorf("css: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	resp, _ = get(t, s, "/static/sentient.js", "127.0.0.1:7357")
	if resp.StatusCode != 200 {
		t.Errorf("js: %d", resp.StatusCode)
	}
	for _, path := range []string{"/alerts", "/nope"} {
		if resp, _ := get(t, s, path, "127.0.0.1:7357"); resp.StatusCode != 404 {
			t.Errorf("%s: %d, want 404 (not built yet)", path, resp.StatusCode)
		}
	}
}

func TestOnlyLocalHosts(t *testing.T) {
	s := newTestServer(t, &fakeSource{st: fullStatus()})
	for host, want := range map[string]int{
		"127.0.0.1:7357":   200,
		"localhost:7357":   200,
		"[::1]:7357":       200,
		"localhost":        200,
		"evil.example:80":  403,
		"10.0.0.5:7357":    403,
		"attacker.com":     403,
		"127.0.0.1.evil.x": 403,
	} {
		if resp, _ := get(t, s, "/", host); resp.StatusCode != want {
			t.Errorf("Host %q: %d, want %d", host, resp.StatusCode, want)
		}
	}
}

func TestActivityHref(t *testing.T) {
	if got := ActivityHref("launch item", "add"); got != "/activity?action=add&resource=launch+item" {
		t.Errorf("ActivityHref = %q", got)
	}
}

package web

import (
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/health"
)

func healthReport() *health.Report {
	return &health.Report{
		CheckedAt:     now,
		HistoryBegins: now.Add(-(30*time.Hour + 28*time.Minute)),
		Daemon: health.Daemon{
			Version: "0.0.1", Started: now.Add(-(12*time.Hour + 32*time.Minute)), CPUPercent: 0.42, CPUWindow: 5 * time.Minute,
			RSSBytes: 31_000_000, Socket: "/run/sentient/sentientd.sock", User: "root", Root: true,
		},
		API: health.API{
			Since: now.Add(-12 * time.Hour), Requests: 18204, PerSecNow: 2.1,
			P50: 4 * time.Millisecond, P95: 22 * time.Millisecond, P99: 61 * time.Millisecond, LatencyCount: 1024,
			Errors: 3, ErrorsByCode: []health.NamedCount{{Name: "Unavailable", Count: 2}, {Name: "DeadlineExceeded", Count: 1}},
			Methods: []health.NamedCount{{Name: "Status", Count: 14880}, {Name: "Health", Count: 3324}},
			Clients: []string{"sen serve", "sen status"},
		},
		Storage: health.Storage{DataDir: "/opt/sentient/data", DBPath: "/opt/sentient/data/sentient.db",
			DBBytes: 1_600_000_000, FreeBytes: 118_000_000_000, SizeBytes: 240_000_000_000},
		Ingest: []health.IngestAgent{
			{Agent: "claude", LastRecord: now.Add(-400 * time.Millisecond), Files: 38},
			{Agent: "codex", LastRecord: now.Add(-50 * time.Hour), Files: 11, Unknown: 0},
		},
		Integrity: health.Integrity{CheckedAt: now.Add(-40 * time.Minute), FullAt: now.Add(-40 * time.Minute),
			Records: 2481906, OK: true, Interval: time.Hour},
		Agents: []health.Agent{{Agent: "claude", Version: "2.1.302", Logs: "~/.claude/projects"}, {Agent: "codex", Logs: "~/.codex/sessions"}},
		Live:   []health.LiveSession{{SessionID: "9c1e0000-x", Agent: "codex", Repo: "git-backlog", LastActivity: now}},
		Sudo:   health.Sudo{Checked: true, Passwordless: []string{"ubuntu"}},
	}
}

func TestHealthPage(t *testing.T) {
	resp, body := get(t, newTestServer(t, &fakeSource{health: healthReport()}), "/health", "127.0.0.1:7357")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{
		`<title>sentient · health</title>`,
		`<a class="icon-btn" href="/health" aria-current="page"`,
		`<main class="wrap sub">`,
		`<a class="back" href="/">&larr; Dashboard</a>`,
		`<span class="okdot" role="img" aria-label="ok"></span>All systems normal`,
		`<strong>1 warning</strong>`,
		`Agent account <span class="mono">'ubuntu'</span> has passwordless sudo`,
		`href="https://github.com/nosaka4i/sentient#passwordless-sudo"`,
		`sentientd · root`,
		`<span class="num">12h 32m</span>`,
		`<span class="num">0.4%</span>`, `avg last 5m`,
		`<span class="num">31 MB</span>`,
		`<span class="num txt mono">/run/sentient/sentientd.sock</span>`,
		`<span class="num txt">ubuntu</span>`, // sudo tile
		`<section class="card muted" aria-labelledby="h-coll">`,
		`<span class="state-off">not set up yet</span>`,
		`gRPC on /run/sentient/sentientd.sock`,
		`<span class="num">18,204</span>`, `2.1 / sec now`,
		`<span class="num">4 <small>ms</small></span>`, `p50 · p95 22 ms · p99 61 ms`,
		`Unavailable 2 · DeadlineExceeded 1`,
		`sen serve · sen status`,
		`<span class="num">Status</span>`, `Status 14,880 · Health 3,324`,
		`<span class="num">1.6 GB</span>`, `data/sentient.db`,
		`not set up yet (kernel capture)`,
		`<span class="num">118 GB</span>`, `of 240 GB`,
		`<span class="v">0.4s</span>`, `<span class="v">38 <small>jsonl</small></span>`,
		`<span class="v">2d 2h</span>`, // codex lag
		`Verified <span class="okt"`, `2,481,906 records checked`,
		`no gap tracking yet`,
		`Agents <span class="count">(2)</span>`,
		`<b>claude code</b>`, `<use href="#i-claude"/></svg>2.1.302`,
		`<a class="tile" href="/session/9c1e0000-x" title="Open the running session">`,
		`<span class="running-dot" aria-hidden="true"></span>1 <small>running</small>`,
		`codex · git-backlog · 9c1e`,
		`<code>sen health</code> (later)`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("health page missing %q", want)
		}
	}
}

func TestHealthPageStates(t *testing.T) {
	r := healthReport()
	r.Integrity = health.Integrity{CheckedAt: now, FullAt: now, OK: false, BadRecordID: 42, Interval: time.Hour}
	r.Sudo = health.Sudo{Reason: "unknown (sentientd not running as root)"}
	r.Ingest[0].Unknown = 3
	r.Storage.FreeBytes = 1_000_000_000
	r.Live = nil
	r.API = health.API{Since: now}
	r.HistoryBegins = time.Time{}
	r.Daemon.CPUWindow = 2 * time.Second
	_, body := get(t, newTestServer(t, &fakeSource{health: r}), "/health", "127.0.0.1:7357")
	for _, want := range []string{
		`okdot bad`, `Integrity check failed`,
		`<strong>3 warnings</strong>`,
		`Hash chain broken`, `3 agent log records of unknown type`, `Disk almost full`,
		`Broken at record #42`,
		`<span class="num txt">unknown</span>`, `unknown (sentientd not running as root)`,
		`none active in the last 5m`,
		`no requests yet`,
		`nothing recorded yet`,
		`measuring (just started)`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("health page missing %q", want)
		}
	}

	r.Integrity = health.Integrity{Interval: time.Hour}
	r.Sudo = health.Sudo{Checked: true}
	_, body = get(t, newTestServer(t, &fakeSource{health: r}), "/health", "127.0.0.1:7357")
	for _, want := range []string{"Not checked yet", `<span class="num txt">none</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("health page missing %q", want)
		}
	}
}

func TestHealthDown(t *testing.T) {
	resp, body := get(t, newTestServer(t, &fakeSource{err: api.ErrUnreachable}), "/health", "127.0.0.1:7357")
	if resp.StatusCode != 503 || !strings.Contains(body, "sentientd is not reachable") {
		t.Errorf("down: %d", resp.StatusCode)
	}
}

func TestFormatters(t *testing.T) {
	for d, want := range map[time.Duration]string{
		45 * time.Second: "45s", 5 * time.Minute: "5m", 2 * time.Hour: "2h",
		12*time.Hour + 32*time.Minute: "12h 32m", 72 * time.Hour: "3d", 76 * time.Hour: "3d 4h",
	} {
		if got := uptime(d); got != want {
			t.Errorf("uptime(%v) = %q, want %q", d, got, want)
		}
	}
	if lag(-time.Second) != "0.0s" || lag(3*time.Minute) != "3m" {
		t.Error("lag")
	}
	if msNum(400*time.Microsecond) != "0.4" || msNum(4*time.Millisecond) != "4" || msNum(61*time.Millisecond) != "61" {
		t.Error("msNum")
	}
	if shortPath("/opt/sentient/data/sentient.db") != "data/sentient.db" || shortPath("x.db") != "x.db" {
		t.Error("shortPath")
	}
	if clock(now.Add(-time.Hour), now) != now.Add(-time.Hour).Format("15:04:05") {
		t.Error("clock today")
	}
	if agentName("codex") != "codex" {
		t.Error("agentName")
	}
}

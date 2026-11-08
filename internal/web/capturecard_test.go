package web

import (
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/health"
)

func tetragonUp() health.Collector {
	return health.Collector{On: true, System: "Tetragon", Platform: "eBPF on Linux", DetailLabel: "socket",
		Detail: "unix:///var/run/tetragon/tetragon.sock", Version: "1.7.1", State: "connected",
		Since: now.Add(-9 * time.Hour), LastEvent: now.Add(-2 * time.Second), PerMinute: 480, QueueSize: 10000,
		Stored: 27716, FirstEvent: now.Add(-100 * time.Hour), Processes: 1204, AgentProcesses: 312, Gaps: 1,
		Latest: health.GapNote{Set: true, From: now.Add(-30 * time.Hour), To: now.Add(-30*time.Hour + time.Minute), Reason: "tetragon unreachable at unix:///x"}}
}

func withCollector(c health.Collector) func(*health.Report) {
	return func(r *health.Report) {
		r.Capture = health.Capture{Enabled: true, Level: "essential", PolicyKnown: true, PolicyLevel: "essential"}
		r.Collector = c
	}
}

func wantAll(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("page lacks %q", w)
		}
	}
}

func wantNone(t *testing.T, body string, bad ...string) {
	t.Helper()
	for _, b := range bad {
		if strings.Contains(body, b) {
			t.Errorf("page has %q", b)
		}
	}
}

func TestKernelCaptureCardWhenConnected(t *testing.T) {
	body := captureBody(t, withCollector(tetragonUp()))
	wantAll(t, body,
		`<span class="state-ok"><span class="okdot"></span>recording</span>`,
		`<span class="lbl"><b>capture system</b></span>`, `<span class="num txt">Tetragon <small>v1.7.1</small></span>`, `eBPF on Linux · capturing since `,
		`<span class="num">27,716</span>`, `since `,
		`<span class="num">2s <small>ago</small></span>`, `~480 / min now`,
		`<span class="num">1,204</span>`, `all time · 312 belong to an agent`,
		`own queue 0 of 10,000 · 0 reconnects`,
		`<span class="lbl"><b>gaps</b> / kernel not recorded</span>`, `latest: tetragon unreachable at unix:///x · `,
		`<span class="lbl"><b>socket</b></span>`, `unix:///var/run/tetragon/tetragon.sock`, `Tetragon&#39;s socket`,
	)
	wantNone(t, body, `not reachable`, `Kernel capture lost`, `warnstate`)
}

func TestKernelCaptureCardIsNotTetragonSpecific(t *testing.T) {
	mac := health.Collector{On: true, System: "eslogger", Platform: "Endpoint Security", DetailLabel: "process", Detail: "eslogger, pid 4182",
		State: "connected", Since: now.Add(-time.Hour), LastEvent: now.Add(-time.Second), PerMinute: 210, QueueSize: 10000, Stored: 9402}
	body := captureBody(t, func(r *health.Report) {
		r.Capture = health.Capture{} // no Tetragon level or policy on macOS
		r.Collector = mac
	})
	wantAll(t, body, `<span class="num txt">eslogger</span>`, `Endpoint Security · capturing since `, `<span class="lbl"><b>process</b></span>`,
		`eslogger, pid 4182`, `eslogger&#39;s process`, `<span class="num">9,402</span>`)
	wantNone(t, body, `<b>level</b>`, `<b>policy</b>`, `Tetragon`, `<small>v`) // no version given: nothing after the name
}

func TestKernelCaptureCardWhileReconnecting(t *testing.T) {
	away := tetragonUp()
	away.State, away.LastError = "reconnecting", "connect to unix:///var/run/tetragon/tetragon.sock: permission denied"
	away.LastEvent = now.Add(-4 * time.Minute)
	away.Latest = health.GapNote{Set: true, From: now.Add(-4 * time.Minute), Open: true, Reason: "tetragon unreachable at x"}

	// past the grace period: red chip, shaded tile, and the warning at the top
	away.Since = now.Add(-4 * time.Minute)
	body := captureBody(t, withCollector(away))
	wantAll(t, body,
		`<span class="state-paused"><span class="okdot bad"></span>reconnecting</span>`,
		`not reachable for 4m`, `permission denied`, `retrying · the kernel is not recording the agent until it is back`,
		`not connected since `, `<span class="num">4m <small>ago</small></span>`, `gap starts `,
		`class="tile wide warnstate"`,
		`Tetragon is not reachable`, `Kernel capture lost`,
	)
	wantNone(t, body, `recording</span>`)

	// inside the grace period (a routine restart): a quiet amber dot, no shading, no warning
	away.Since = now.Add(-10 * time.Second)
	body = captureBody(t, withCollector(away))
	wantAll(t, body, `<span class="state-ok"><span class="okdot warn"></span>reconnecting</span>`, `not reachable for 10s`)
	wantNone(t, body, `class="tile wide warnstate"`, `Kernel capture lost`, `is not reachable</span>`)
}

func TestKernelCaptureCardStartingUpAndNoEventsYet(t *testing.T) {
	c := tetragonUp()
	c.State, c.LastEvent, c.Stored, c.Processes, c.AgentProcesses, c.Gaps, c.Latest = "connecting", time.Time{}, 0, 0, 0, 0, health.GapNote{}
	body := captureBody(t, withCollector(c))
	wantAll(t, body, `<span class="state-ok"><span class="okdot"></span>connecting</span>`, `eBPF on Linux · connecting`,
		`none since sentientd started`, `none yet`)
	wantNone(t, body, `not reachable`)
}

func TestKernelCaptureCardIdleAndGapEnds(t *testing.T) {
	c := tetragonUp()
	c.PerMinute = 0
	c.Latest = health.GapNote{Set: true, Open: true, From: now.Add(-10 * time.Minute), Reason: strings.Repeat("a long reason ", 10)}
	body := captureBody(t, withCollector(c))
	wantAll(t, body, `none in the last minute`, `still open`, `…`)
}

func TestKernelCaptureCardCountsWhatSentientLoses(t *testing.T) {
	c := tetragonUp()
	c.QueueWaiting, c.Lost, c.Reconnects = 3, 4, 1
	body := captureBody(t, withCollector(c))
	wantAll(t, body, `<span class="num">4</span>`, `own queue 3 of 10,000 · 1 reconnect`, `class="tile warnstate"`,
		`<span class="state-ok"><span class="okdot warn"></span>recording</span>`)
}

func TestKernelCaptureCardWhileTheDatabaseIsBusy(t *testing.T) {
	body := captureBody(t, func(r *health.Report) {
		withCollector(tetragonUp())(r)
		r.CatchingUp = true
		r.Collector.Stored, r.Collector.Processes, r.Collector.Gaps, r.Collector.Latest = 0, 0, 0, health.GapNote{}
	})
	if n := strings.Count(body, `still counting`); n != 3 {
		t.Errorf("%d tiles say they are still counting, want 3 (events, processes, gaps)", n)
	}
	wantAll(t, body, `processing logs since the last startup`)
}

func TestKernelCaptureCardPausedKeepsTheCollectorTiles(t *testing.T) {
	body := captureBody(t, func(r *health.Report) {
		withCollector(tetragonUp())(r)
		r.Storage.Paused, r.Storage.PausedSince, r.Storage.UsedPct, r.Storage.HighPct, r.Storage.LowPct = true, now.Add(-10*time.Minute), 81, 80, 75
	})
	wantAll(t, body, `<span class="state-paused"><span class="okdot bad"></span>recording paused</span>`, `<b>paused</b> since`, `<b>capture system</b>`)
}

func TestKernelCaptureCardAgentLogsOnlyIsNotAWarning(t *testing.T) {
	r := healthReport()
	r.Sudo = health.Sudo{Checked: true} // nothing else to warn about
	r.Capture, r.Collector = health.Capture{}, health.Collector{}
	_, body := get(t, newTestServer(t, &fakeSource{health: r}), "/health", "127.0.0.1:7357")
	wantAll(t, body, `<span class="state-off">agent logs only</span>`, `All systems normal`, `no kernel collector started`)
	wantNone(t, body, `okdot warn`, `okdot bad`, `<b>capture system</b>`)
}

func TestVersionLabelAndTrunc(t *testing.T) {
	for in, want := range map[string]string{"1.7.1": "v1.7.1", "v1.7.1": "v1.7.1", "": "", "dev": "dev"} {
		if got := versionLabel(in); got != want {
			t.Errorf("versionLabel(%q) = %q, want %q", in, got, want)
		}
	}
	if trunc("short", 10) != "short" || trunc("abcdefghij", 5) != "abcd…" {
		t.Errorf("trunc: %q %q", trunc("short", 10), trunc("abcdefghij", 5))
	}
}

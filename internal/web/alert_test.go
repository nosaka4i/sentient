package web

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/health"
)

// pausedReport is the healthy report with recording paused at 81% used.
func pausedReport() *health.Report {
	r := healthReport()
	r.Capture = health.Capture{Enabled: true, Level: "complete", PolicyKnown: true, PolicyLevel: "complete"}
	r.Storage.UsedPct, r.Storage.HighPct, r.Storage.LowPct = 81, 80, 75
	r.Storage.Paused, r.Storage.PausedSince = true, now.Add(-10*time.Minute)
	return r
}

// countingSource counts Health calls and answers with a report the test can change.
type countingSource struct {
	*fakeSource
	calls atomic.Int32
	rep   atomic.Pointer[health.Report]
	err   atomic.Pointer[error]
}

func (c *countingSource) Health(context.Context) (*health.Report, error) {
	c.calls.Add(1)
	if e := c.err.Load(); e != nil {
		return nil, *e
	}
	return c.rep.Load(), nil
}

func sessionsServer(t *testing.T, rep *health.Report) *Server {
	t.Helper()
	return newTestServer(t, &fakeSource{health: rep, list: sampleSessionList(), window: sampleWindow()})
}

func TestBannerOnEveryPageWhilePaused(t *testing.T) {
	s := sessionsServer(t, pausedReport())
	for _, path := range []string{"/sessions", "/prompts", "/health"} {
		resp, body := get(t, s, path, "localhost")
		if resp.StatusCode != 200 {
			t.Fatalf("%s: status %d", path, resp.StatusCode)
		}
		for _, want := range []string{
			`<div class="paused-banner" role="alert">`,
			`<b>Recording is paused:</b> the disk is 81% used. Nothing is recorded until it is below 75%.`,
			`<a class="fix" href="/health">Open Health &rarr;</a>`,
			`aria-label="Health: recording paused"`, `<span class="pip bad"></span>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %q", path, want)
			}
		}
	}
}

func TestWarningHasADotButNoBanner(t *testing.T) {
	r := healthReport() // one warning: passwordless sudo
	_, body := get(t, sessionsServer(t, r), "/sessions", "localhost")
	if strings.Contains(body, "paused-banner") {
		t.Error("banner without a pause")
	}
	for _, want := range []string{`aria-label="Health: 1 warning"`, `<span class="pip"></span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("lacks %q", want)
		}
	}
	if strings.Contains(body, `pip bad`) {
		t.Error("a warning's dot is red")
	}
}

func TestNothingToSayMeansNoDotAndNoBanner(t *testing.T) {
	r := healthReport()
	r.Sudo = health.Sudo{Checked: true}
	_, body := get(t, sessionsServer(t, r), "/sessions", "localhost")
	if strings.Contains(body, `class="pip`) || strings.Contains(body, "paused-banner") {
		t.Error("a dot or banner on a healthy system")
	}
	if !strings.Contains(body, `aria-label="Health: sentient&#39;s own status"`) {
		t.Errorf("icon label:\n%.400s", body)
	}
}

func TestDownPageHasNoBanner(t *testing.T) {
	s := newTestServer(t, &fakeSource{err: errors.New("unreachable")})
	s.setAlert(pausedReport()) // a cached pause must not show on the page that says sentientd is down
	resp, body := get(t, s, "/health", "localhost")
	if resp.StatusCode == 200 || strings.Contains(body, "paused-banner") {
		t.Errorf("status %d, banner in the down page", resp.StatusCode)
	}
}

func TestHealthPageWhilePaused(t *testing.T) {
	_, body := get(t, sessionsServer(t, pausedReport()), "/health", "localhost")
	for _, want := range []string{
		`okdot bad`, `Recording paused`, // the headline names the problem
		`<strong>`, `Recording paused: the disk is 81% used`,
		`resumes at 75% used`, `Nothing is deleted automatically`,
		`<span class="state-paused"><span class="okdot bad"></span>recording paused</span>`,
		`<span class="num">81 <small>%</small></span>`, `pauses at 80%`,
		`<b>paused</b> since`, `one gap recorded · resumes at 75%`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("health page lacks %q", want)
		}
	}
	if strings.Contains(body, "Integrity check failed") {
		t.Error("the headline blames the integrity check")
	}
}

func TestAlertIsAskedOnceAndRefreshedInTheBackground(t *testing.T) {
	src := &countingSource{fakeSource: &fakeSource{list: sampleSessionList(), window: sampleWindow()}}
	src.rep.Store(healthReport())
	s := newTestServer(t, src)
	clock := now
	s.now = func() time.Time { return clock }

	for i := 0; i < 3; i++ {
		get(t, s, "/sessions", "localhost")
	}
	if n := src.calls.Load(); n != 1 {
		t.Fatalf("Health asked %d times for three pages", n)
	}

	// a paused report appears; the cached answer is still used until it is stale
	src.rep.Store(pausedReport())
	_, body := get(t, s, "/sessions", "localhost")
	if strings.Contains(body, "paused-banner") {
		t.Error("banner before the cache went stale")
	}

	clock = clock.Add(alertTTL + time.Second)
	_, body = get(t, s, "/sessions", "localhost") // this page still shows the old answer and starts a refresh
	if strings.Contains(body, "paused-banner") {
		t.Error("the page that triggers a refresh waited for it")
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.notice() == nil || !s.notice().Paused {
		if time.Now().After(deadline) {
			t.Fatal("background refresh never arrived")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, body = get(t, s, "/sessions", "localhost"); !strings.Contains(body, "paused-banner") {
		t.Error("banner missing after the refresh")
	}
}

func TestAlertKeepsTheLastAnswerWhenSentientdDoesNotAnswer(t *testing.T) {
	src := &countingSource{fakeSource: &fakeSource{}}
	src.rep.Store(pausedReport())
	s := newTestServer(t, src)
	if a := s.notice(); a == nil || !a.Paused {
		t.Fatalf("first look = %+v", a)
	}
	boom := errors.New("socket closed")
	src.err.Store(&boom)
	if a := s.refreshAlert(time.Second); a == nil || !a.Paused {
		t.Errorf("after a failed refresh = %+v, want the last answer", a)
	}
	// never answered: nothing to show
	src2 := &countingSource{fakeSource: &fakeSource{}}
	src2.err.Store(&boom)
	if a := newTestServer(t, src2).notice(); a != nil {
		t.Errorf("never answered, got %+v", a)
	}
}

func TestAlertFrom(t *testing.T) {
	if alertFrom(nil, now) != nil {
		t.Error("nil report")
	}
	a := alertFrom(pausedReport(), now)
	if !a.Paused || a.Used != 81 || a.ResumeAt != 75 || a.Label != "Health: recording paused" || a.Warnings != 2 || !a.Critical { // the pause and the sudo warning
		t.Errorf("paused = %+v", a)
	}
	r := healthReport()
	r.Sudo = health.Sudo{}
	if a := alertFrom(r, now); a.Warnings != 0 || a.Paused || a.Critical || a.Label != "Health: sentient's own status" {
		t.Errorf("healthy = %+v", a)
	}
	r.Sudo = health.Sudo{Checked: true, Passwordless: []string{"a", "b"}}
	if a := alertFrom(r, now); a.Warnings != 2 || a.Label != "Health: 2 warnings" || a.Critical {
		t.Errorf("two warnings = %+v", a)
	}
}

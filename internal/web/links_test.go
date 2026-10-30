package web

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var hrefRE = regexp.MustCompile(`href="([^"]+)"`)

// TestNoDeadInternalLinks renders every page with sample data and follows
// each link that points inside the app, so a link to a page that doesn't
// exist (the old "All alerts" link) fails here, not in a browser.
func TestNoDeadInternalLinks(t *testing.T) {
	ad, _ := alertDetailFixture()
	src := &fakeSource{
		st: fullStatus(), window: sampleWindow(), list: sampleSessionList(), activity: sampleActivity(),
		health: healthReport(), detail: sampleDetail(), session: sampleSession(), alerts: sampleAlerts(), alert: ad,
		agents: sampleAgents(), agent: sampleAgentDetail(), timeline: sampleTimeline(),
	}
	s := newTestServer(t, src)
	pages := []string{"/", "/sessions", "/prompts", "/activity", "/health", "/prompt/7", "/session/404dabcd", "/alerts", "/alert/9",
		"/agents", "/agent/root%2Fa", "/agent/root%2Fa?view=timeline&q=git",
		"/alerts?status=all&sev=high",
		"/activity?resource=file&action=update&days=7", "/sessions?days=0&sort=sess:asc", "/prompts?session=404dabcd"}
	checked := map[string]bool{}
	links := 0
	for _, page := range pages {
		resp, body := get(t, s, page, "localhost")
		if resp.StatusCode != 200 {
			t.Errorf("%s = %d", page, resp.StatusCode)
			continue
		}
		for _, m := range hrefRE.FindAllStringSubmatch(body, -1) {
			href := strings.ReplaceAll(m[1], "&amp;", "&")
			if !strings.HasPrefix(href, "/") || strings.HasPrefix(href, "//") {
				continue // external, fragment-only or relative
			}
			u, err := url.Parse(href)
			if err != nil {
				t.Errorf("%s: bad link %q", page, href)
				continue
			}
			u.Fragment = ""
			target := u.String()
			if checked[target] {
				continue
			}
			checked[target] = true
			links++
			if r, _ := get(t, s, target, "localhost"); r.StatusCode == 404 || r.StatusCode >= 500 {
				t.Errorf("%s links to %s, which returns %d", page, target, r.StatusCode)
			}
		}
	}
	if links < 40 {
		t.Errorf("only %d distinct internal links found; the pages or the matcher changed", links)
	}
}

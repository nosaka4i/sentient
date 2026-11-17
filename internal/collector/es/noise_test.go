package es

import (
	"strings"
	"testing"

	"github.com/nosaka4i/sentient/internal/collector/tetragon/policy"
)

func TestAgentPrefixes(t *testing.T) {
	got := AgentPrefixes([]string{"/Users/dev"})
	for _, want := range []string{
		"/Users/dev/Library/Application Support/Claude/claude-code/",
		"/Users/dev/.local/share/claude/versions/",
		"/opt/homebrew/bin/claude",
	} {
		found := false
		for _, g := range got {
			found = found || g == want
		}
		if !found {
			t.Errorf("%s missing from %v", want, got)
		}
	}
	// the desktop app's bundled Claude Code is recognized by a path with a version in it
	if !policy.IsAgent(got, "/Users/dev/Library/Application Support/Claude/claude-code/2.1.281/claude.app/Contents/MacOS/claude") {
		t.Error("the bundled Claude Code is not an agent")
	}
	if policy.IsAgent(got, "/Users/dev/Library/Application Support/Claude/Cache/x") {
		t.Error("the app's cache counts as an agent")
	}
}

func TestNoiseMatchesWhatTheCaptureShowed(t *testing.T) {
	noise := NoisePaths([]string{"/Users/dev"})
	for _, c := range []struct {
		path   string
		noisy  bool
		reason string
	}{
		{"/System/Volumes/Preboot/x", true, "system files"},
		{"/usr/local/go/src/fmt/print.go", true, "system files"},
		{"/private/var/folders/26/abc/T/go-build1/b001/_pkg_.a", true, "temporary and cache folders"},
		{"/Users/dev/Library/Caches/go-build/aa/x", true, "caches"},
		{"/Users/dev/.claude/projects/p/s.jsonl", true, "the agent's own state"},
		{"/Users/dev/work/pkg/mod/github.com/x/y@v1/y.go", true, "Go module cache"}, // GOPATH is ~/work
		{"/Users/dev/proj/node_modules/left-pad/index.js", true, "npm packages"},
		{"/Users/dev/proj/main.go", false, ""},
		{"/Users/dev/work/src/github.com/nosaka4i/sentient/cmd/sen/main.go", false, ""},
		{"/private/tmp/outside-test.txt", false, ""},
		{"/Users/dev/.ssh/id_ed25519", false, ""},
	} {
		n, ok := policy.Match(noise, c.path)
		if ok != c.noisy || (ok && n.Category != c.reason) {
			t.Errorf("%s: noise=%v (%s), want %v (%s)", c.path, ok, n.Category, c.noisy, c.reason)
		}
	}
}

func TestSensitiveAndMutes(t *testing.T) {
	homes := []string{"/Users/dev"}
	sens := SensitivePaths(homes)
	has := func(list []string, want string) bool {
		for _, s := range list {
			if s == want {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"/Users/dev/.ssh/", "/Library/Keychains/", "/Users/dev/Library/Keychains/"} {
		if !has(sens, want) {
			t.Errorf("%s missing from the sensitive list", want)
		}
	}
	mutes := Mutes(NoisePaths(homes), sens)
	// kernel mutes are prefixes: /Library/ holds the keychains, so it is not muted there
	if has(mutes, "/Library/") {
		t.Error("/Library/ is muted in the kernel although it holds the keychains")
	}
	for _, want := range []string{"/System/", "/usr/", "/private/var/folders/", "/Users/dev/Library/Caches/", "/Users/dev/.claude/"} {
		if !has(mutes, want) {
			t.Errorf("%s is not muted in the kernel", want)
		}
	}
	for _, m := range mutes {
		if strings.Contains(m, "pkg/mod") || strings.Contains(m, "node_modules") {
			t.Errorf("an anywhere entry %q became a kernel mute", m)
		}
		for _, s := range sens {
			if strings.HasPrefix(s, m) {
				t.Errorf("mute %q covers the sensitive path %q", m, s)
			}
		}
	}
}

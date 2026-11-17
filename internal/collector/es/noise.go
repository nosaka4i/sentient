package es

import (
	"path/filepath"
	"strings"

	"github.com/nosaka4i/sentient/internal/collector"
	"github.com/nosaka4i/sentient/internal/collector/tetragon/policy"
)

// AgentPrefixes is where the agent's binaries live on the Mac, for the
// extension's tree and sentientd's own checks: the Claude desktop app's bundled
// Claude Code (its path holds a version, so the whole folder counts), the
// native command-line install, and the usual global places. An entry ending in
// "/" matches everything under it; any other entry is one file.
func AgentPrefixes(homes []string) []string {
	out := policy.AgentPrefixes(homes) // /usr/local/bin/claude, ~/.local/bin/claude, ~/.local/share/claude/versions/
	out = append(out, "/opt/homebrew/bin/claude")
	for _, h := range homes {
		out = append(out, filepath.Join(h, "Library/Application Support/Claude/claude-code")+"/")
	}
	return out
}

// system locations whose opens are noise: libraries, binaries and frameworks
// every process loads, and the per-user temp and cache folders of the build
// tools (a cold `go test ./...` opened 19,000 files under /private/var/folders
// and 11,000 under /usr/local/go, 1,750 in the project; see
// docs/mac-collector-design.md, open question 5).
var systemNoise = []policy.NoisePath{
	{Path: "/System/", Category: "system files"},
	{Path: "/usr/", Category: "system files"},
	{Path: "/bin/", Category: "system files"},
	{Path: "/sbin/", Category: "system files"},
	{Path: "/Library/", Category: "system files"},
	{Path: "/dev/", Category: "devices"},
	{Path: "/opt/homebrew/", Category: "Homebrew"},
	{Path: "/private/var/db/", Category: "system files"},
	{Path: "/private/var/folders/", Category: "temporary and cache folders"},
	// every process opens the root directory; only that one path
	{Path: "/", Category: "the root directory", Exact: true},
}

// userNoise is relative to a home: the agent's own state, caches, and tool
// state that is not evidence.
var userNoise = []policy.NoisePath{
	{Path: ".claude/", Category: "the agent's own state"},
	{Path: "Library/Caches/", Category: "caches"},
	{Path: "Library/Application Support/go/", Category: "Go tool state"},
	{Path: ".cache/", Category: "caches"},
	{Path: ".CFUserTextEncoding", Category: "system files"},
}

// anywhere are dependency caches whose folder the user chooses (Go's module
// cache sits under whatever GOPATH is set), so they match by a path segment.
// They are applied by sentientd's filter; the kernel mute list is prefixes only.
var anywhere = []policy.NoisePath{
	{Path: "/pkg/mod/", Category: "Go module cache", Anywhere: true},
	{Path: "/node_modules/", Category: "npm packages", Anywhere: true},
	{Path: "/.cargo/registry/", Category: "Rust crate cache", Anywhere: true},
	{Path: "/.gradle/caches/", Category: "Gradle cache", Anywhere: true},
	{Path: "/.m2/repository/", Category: "Maven cache", Anywhere: true},
}

// NoisePaths is every location whose opens sentientd leaves out below the
// `complete` level on the Mac, for the given homes.
func NoisePaths(homes []string) []policy.NoisePath {
	out := append([]policy.NoisePath(nil), systemNoise...)
	for _, h := range homes {
		for _, n := range userNoise {
			out = append(out, policy.NoisePath{Path: filepath.Join(h, n.Path) + slashIfDir(n.Path), Category: n.Category})
		}
	}
	return append(out, anywhere...)
}

// Mutes are the prefixes the extension mutes in the kernel: the noise entries
// that are plain prefixes. An "anywhere" entry cannot be a kernel mute, and
// neither can a prefix that holds a sensitive path (the kernel mutes a whole
// prefix, with no exceptions): /Library/ holds the keychains. Those opens
// still reach sentientd, whose filter drops the noise and keeps the sensitive
// ones.
func Mutes(noise []policy.NoisePath, sensitive []string) []string {
	var out []string
next:
	for _, n := range noise {
		if n.Anywhere || n.Exact {
			continue
		}
		for _, s := range sensitive {
			if strings.HasPrefix(s, n.Path) {
				continue next
			}
		}
		out = append(out, n.Path)
	}
	return out
}

// SensitivePaths is the core list plus the Mac's keychains. The keychains sit
// inside ~/Library, which is muted, so these entries are what keeps their opens.
func SensitivePaths(homes []string) []string {
	out := collector.SensitivePaths(homes)
	out = append(out, "/Library/Keychains/")
	for _, h := range homes {
		out = append(out, filepath.Join(h, "Library/Keychains")+"/")
	}
	return out
}

func slashIfDir(p string) string {
	if p != "" && p[len(p)-1] == '/' {
		return "/"
	}
	return ""
}

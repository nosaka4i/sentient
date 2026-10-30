// Package policy renders the Tetragon TracingPolicy that makes Tetragon report
// an agent's process tree. It has no dependency on Tetragon's API, so the sen
// client can print the policy without linking the collector.
package policy

import (
	_ "embed"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

//go:embed sentient.yaml.tmpl
var source string

var tmpl = template.Must(template.New("policy").Parse(source))

// AgentPrefixes is where each home's agent binaries live, for the collector's
// own user-space filter. An entry ending in "/" matches everything under it
// (Claude's path contains its version and changes on every auto-update); any
// other entry is a single file.
func AgentPrefixes(homes []string) []string {
	out := []string{"/usr/local/bin/claude"}
	for _, h := range homes {
		out = append(out, h+"/.local/bin/claude", h+"/.local/share/claude/versions/")
	}
	return out
}

// IsAgent reports whether binary is one of the agent paths in prefixes (see
// AgentPrefixes).
func IsAgent(prefixes []string, binary string) bool {
	for _, p := range prefixes {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(binary, p) {
				return true
			}
		} else if binary == p {
			return true
		}
	}
	return false
}

// systemBinaries are agent installs outside any home directory.
var systemBinaries = []string{"/usr/local/bin/claude"}

// Discover lists the agent binaries installed for the given homes: the
// launcher link and every versioned binary Claude has left behind. Tetragon
// 1.7.1 cannot follow children from a prefix match, so the policy has to name
// exact files; an agent update adds a new one, which is why the policy is
// generated and has to be generated again after an update.
func Discover(homes []string) []string {
	seen := map[string]bool{}
	add := func(p string) {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			seen[p] = true
		}
	}
	for _, p := range systemBinaries {
		add(p)
	}
	for _, h := range homes {
		add(filepath.Join(h, ".local/bin/claude"))
		if es, err := os.ReadDir(filepath.Join(h, ".local/share/claude/versions")); err == nil {
			for _, e := range es {
				add(filepath.Join(h, ".local/share/claude/versions", e.Name()))
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Render returns the policy for the given agent binaries (see Discover) at the
// Complete level (every event, nothing left out); RenderWith takes a level,
// matched as exact paths with their children followed.
func Render(binaries []string) (string, error) {
	return RenderWith(Options{Binaries: binaries, Verbosity: Complete})
}

package policy

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Verbosity is how much of the kernel's stream the policy asks for (see
// docs/capture-verbosity.md).
type Verbosity string

const (
	// Essential leaves out the opens of build caches, toolchains and system
	// libraries: noise that is not evidence. The default.
	Essential Verbosity = "essential"
	// Extended asks for everything; sentientd counts the noise per command
	// instead of storing it.
	Extended Verbosity = "extended"
	// Complete asks for everything and sentientd stores all of it.
	Complete Verbosity = "complete"
)

// ParseVerbosity reads a level name; the empty string is Essential.
func ParseVerbosity(s string) (Verbosity, error) {
	switch v := Verbosity(strings.ToLower(strings.TrimSpace(s))); v {
	case "":
		return Essential, nil
	case Essential, Extended, Complete:
		return v, nil
	}
	return "", fmt.Errorf("unknown verbosity %q (use essential, extended or complete)", s)
}

// NoisePath is a location whose file opens are noise, and its named category
// ("Go build cache", "system libraries"): `extended` groups the counts by it.
type NoisePath struct {
	Path     string
	Category string
}

// systemNoise is always excluded at Essential: loaded by every program on exec.
var systemNoise = []NoisePath{
	{"/usr/lib", "system libraries"},
	{"/usr/lib64", "system libraries"},
	{"/lib", "system libraries"},
	{"/lib64", "system libraries"},
	{"/usr/share", "shared system files"},
	{"/etc/ld.so", "the dynamic linker's cache"},
}

// procNoise is /proc/<pid>/stat and statm: an agent polling its own process.
// The rest of /proc stays recorded (/proc/<pid>/environ and mem are how one
// process reads another's secrets), so it is excluded by two selectors in the
// template, not by a path here.
const procPrefix = "/proc/"

// Env is what noise detection reads from the machine, so tests can supply it.
type Env struct {
	Getenv func(string) string
	// GoEnv returns `go env` values (GOROOT, GOCACHE, GOMODCACHE); nil or an
	// empty map when Go is not installed.
	GoEnv func() map[string]string
	// IsDir reports whether a directory exists.
	IsDir func(string) bool
	// ReadFile reads a user's extra noise paths.
	ReadFile func(string) ([]byte, error)
}

// SystemEnv is the real machine.
func SystemEnv() Env {
	return Env{
		Getenv: os.Getenv,
		GoEnv: func() map[string]string {
			out, err := exec.Command("go", "env", "GOROOT", "GOCACHE", "GOMODCACHE").Output()
			if err != nil {
				return nil
			}
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if len(lines) < 3 {
				return nil
			}
			return map[string]string{"GOROOT": lines[0], "GOCACHE": lines[1], "GOMODCACHE": lines[2]}
		},
		IsDir:    func(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() },
		ReadFile: os.ReadFile,
	}
}

// ExtrasFile is where a user lists more noise locations, one absolute prefix
// per line (# starts a comment), relative to their home.
const ExtrasFile = ".config/sentient/noise-paths"

// DetectNoise finds the locations whose opens are noise on this machine, for
// the given homes: build caches and toolchains of the ecosystems found, the
// system library directories, and what each home lists in ExtrasFile. A path
// that has to be quoted into YAML safely (no quotes, backslashes or newlines)
// and is absolute is kept; the rest is dropped.
func DetectNoise(homes []string, env Env) []NoisePath {
	seen := map[string]NoisePath{}
	add := func(p, why string, mustExist bool) {
		p = filepath.Clean(strings.TrimSpace(p))
		if !filepath.IsAbs(p) || p == "/" || strings.ContainsAny(p, "\"\\\n\r") {
			return
		}
		if mustExist && !env.IsDir(p) {
			return
		}
		if _, ok := seen[p]; !ok {
			seen[p] = NoisePath{p, why}
		}
	}
	for _, n := range systemNoise {
		add(n.Path, n.Category, false)
	}
	if goenv := env.GoEnv(); goenv != nil {
		add(goenv["GOROOT"], "Go toolchain", true)
		add(goenv["GOCACHE"], "Go build cache", true)
		add(goenv["GOMODCACHE"], "Go module cache", true)
	}
	for _, h := range homes {
		cargo := env.Getenv("CARGO_HOME")
		if cargo == "" {
			cargo = filepath.Join(h, ".cargo")
		}
		add(filepath.Join(cargo, "registry"), "Rust crate cache", true)
		add(filepath.Join(cargo, "git"), "Rust git cache", true)
		add(filepath.Join(h, ".npm/_cacache"), "npm cache", true)
		nvm := env.Getenv("NVM_DIR")
		if nvm == "" {
			nvm = filepath.Join(h, ".nvm")
		}
		add(filepath.Join(nvm, "versions"), "Node toolchains", true)
		add(filepath.Join(h, ".cache/pip"), "pip cache", true)

		if b, err := env.ReadFile(filepath.Join(h, ExtrasFile)); err == nil {
			sc := bufio.NewScanner(strings.NewReader(string(b)))
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if i := strings.Index(line, "#"); i >= 0 {
					line = strings.TrimSpace(line[:i])
				}
				if line != "" {
					add(line, "listed in "+filepath.Join("~", ExtrasFile), false)
				}
			}
		}
	}
	out := make([]NoisePath, 0, len(seen))
	for _, n := range seen {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// comment is the policy's header lines about the level: what was left out, so
// the installed file explains itself.
func comment(v Verbosity, noise []NoisePath) string {
	var b strings.Builder
	switch v {
	case Essential:
		b.WriteString("# Verbosity: essential. Opens of these locations are not reported:\n")
		for _, n := range noise {
			fmt.Fprintf(&b, "#   %-40s (%s)\n", n.Path, n.Category)
		}
		b.WriteString("#   /proc/<pid>/stat, /proc/<pid>/statm   (an agent polling itself)\n")
	case Extended:
		b.WriteString("# Verbosity: extended. Every event is reported; sentientd counts the noise per command and stores the rest.\n")
	default:
		b.WriteString("# Verbosity: complete. Every event is reported; sentientd stores all of them.\n")
	}
	return b.String()
}

// Options are what Render writes.
type Options struct {
	Binaries  []string
	Verbosity Verbosity // empty is Essential
	Noise     []NoisePath
}

// RenderWith returns the policy for the given options. At Essential the
// file_open hook leaves out the noise locations; at the other levels it asks
// for every open.
func RenderWith(o Options) (string, error) {
	if o.Verbosity == "" {
		o.Verbosity = Essential
	}
	if _, err := ParseVerbosity(string(o.Verbosity)); err != nil {
		return "", err
	}
	if len(o.Binaries) == 0 {
		return "", fmt.Errorf("policy: no agent binaries")
	}
	for _, b := range o.Binaries {
		if !strings.HasPrefix(b, "/") || strings.ContainsAny(b, "\"\\\n\r") {
			return "", fmt.Errorf("policy: %q is not an absolute path", b)
		}
	}
	excluded := make([]string, 0, len(o.Noise)+1)
	for _, n := range o.Noise {
		if !strings.HasPrefix(n.Path, "/") || strings.ContainsAny(n.Path, "\"\\\n\r") {
			return "", fmt.Errorf("policy: noise path %q is not an absolute path", n.Path)
		}
		excluded = append(excluded, n.Path)
	}
	excluded = append(excluded, procPrefix) // /proc is handled by the second selector
	var b strings.Builder
	err := tmpl.Execute(&b, struct {
		Binaries  []string
		Essential bool
		Excluded  []string
		Comment   string
	}{o.Binaries, o.Verbosity == Essential, excluded, comment(o.Verbosity, o.Noise)})
	if err != nil {
		return "", err
	}
	return b.String(), nil
}

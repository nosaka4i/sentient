// Package background says which commands a coding agent runs on its own to do
// its job (reading the repo's git state, helper runs of its own binary),
// without a tool call in its log. The kernel sees them; they belong to the
// agent process, not to a prompt or session, and are shown grouped per command.
// Anything not matched here stays visible as its own unreported command.
//
// To teach sentient a new one, add a rule to rules.
package background

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
)

// RulesVersion changes whenever the rules do. A process is checked once per
// version, so a new rule applies to what was already recorded.
const RulesVersion = 4

// versionBinary matches an agent binary named by its version, like 2.1.305.
var versionBinary = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// gitReadOnly are the git subcommands the agent polls: they only read.
var gitReadOnly = map[string]bool{
	"status": true, "rev-parse": true, "config": true, "remote": true, "ls-files": true,
}

// rules each take the program name and its arguments and return the group key
// ("" when the rule does not apply).
var rules = []func(prog string, args []string) string{
	// the GitHub CLI listing issues: Claude Code runs `gh issue list -R
	// anthropics/claude-code --author @me --state closed ...` on its own when it
	// (re)starts. It reaches the network with the user's login, which the agent's
	// page shows from the kernel's events; it is listed there, not hidden.
	func(prog string, args []string) string {
		if prog != "gh" {
			return ""
		}
		var words []string
		for i := 0; i < len(args) && len(words) < 2; i++ {
			switch a := args[i]; {
			case a == "-R" || a == "--repo":
				i++ // takes a value
			case strings.HasPrefix(a, "-"):
			default:
				words = append(words, a)
			}
		}
		if len(words) == 2 && words[0] == "issue" && words[1] == "list" {
			return "gh issue list"
		}
		return ""
	},
	// git queries the agent makes about the working tree
	func(prog string, args []string) string {
		if prog != "git" {
			return ""
		}
		for i := 0; i < len(args); i++ {
			switch a := args[i]; {
			case a == "-c" || a == "-C":
				i++ // takes a value
			case strings.HasPrefix(a, "-"):
			case gitReadOnly[a]:
				return gitKey(a, args[i+1:])
			default:
				return "" // some other subcommand
			}
		}
		return ""
	},
}

// gitKey names a git command group: the subcommand, and for `config` and
// `remote` what it does (get-url, --get), which tells apart commands that
// would otherwise share a row.
func gitKey(sub string, rest []string) string {
	switch sub {
	case "config":
		for _, a := range rest {
			if a == "--get" || a == "--get-all" || a == "--get-regexp" {
				return "git config " + a
			}
		}
		return "" // a config write is not housekeeping
	case "remote":
		if len(rest) > 0 && (rest[0] == "get-url" || rest[0] == "show") {
			return "git remote " + rest[0]
		}
		return "" // add/remove/set-url change the repo
	}
	return "git " + sub
}

// Key says whether a command line is a known background command, and gives the
// key its runs are grouped under.
func Key(command string) (string, bool) {
	f := strings.Fields(command)
	if len(f) == 0 {
		return "", false
	}
	prog := filepath.Base(f[0])
	for _, rule := range rules {
		if k := rule(prog, f[1:]); k != "" {
			return k, true
		}
	}
	return "", false
}

// Claude Code's shell snapshot replaces grep and find with its own binary run
// under another name (`exec -a ugrep|bfs <claude> <injected flags> <args>`).
// The collector keeps the arguments but not argv[0], so these are recognized by
// the flags the wrapper injects.
const (
	grepFlagsPrefix = `["-G","--ignore-files","--hidden","-I"`
	findFlagsPrefix = `["-S","dfs","-regextype","findutils-default"`
)

// ToolRunPrefixes are the starts of the stored argument lists of the redirected
// grep and find, for a database to match on.
func ToolRunPrefixes() []string { return []string{grepFlagsPrefix, findFlagsPrefix} }

// IsToolRun says whether a run of the agent's own binary is a shell command
// the snapshot redirected (grep, find), not an agent. argvJSON is the JSON
// array the collector stored.
func IsToolRun(argvJSON string) bool {
	return strings.HasPrefix(argvJSON, grepFlagsPrefix) || strings.HasPrefix(argvJSON, findFlagsPrefix)
}

// EffectiveCommand is the command a process ran as the agent's log would
// show it: for a redirected grep or find, the command the agent typed (the
// injected flags removed), else the process as it was started.
func EffectiveCommand(binary, argvJSON string) string {
	var a []string
	if err := json.Unmarshal([]byte(argvJSON), &a); err != nil {
		return CommandLine(binary, argvJSON)
	}
	switch {
	case strings.HasPrefix(argvJSON, grepFlagsPrefix):
		i := 4
		for i < len(a) && strings.HasPrefix(a[i], "--exclude-dir=") {
			i++
		}
		return strings.TrimSpace("grep " + strings.Join(a[i:], " "))
	case strings.HasPrefix(argvJSON, findFlagsPrefix):
		return strings.TrimSpace("find " + strings.Join(a[4:], " "))
	}
	return CommandLine(binary, argvJSON)
}

// CommandLine is a process as one would type it: the program name and its
// arguments (argvJSON is the JSON array the collector stored).
func CommandLine(binary, argvJSON string) string {
	var a []string
	_ = json.Unmarshal([]byte(argvJSON), &a)
	return strings.TrimSpace(filepath.Base(binary) + " " + strings.Join(a, " "))
}

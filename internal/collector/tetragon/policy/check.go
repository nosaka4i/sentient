package policy

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultPolicyFile is where `sen policy` output is installed.
const DefaultPolicyFile = "/etc/tetragon/tetragon.tp.d/sentient.yaml"

// clockTicks is the unit of the start time in /proc/<pid>/stat. It is 100 on
// every Linux architecture sentient supports.
const clockTicks = 100

// CheckOptions says what Check looks at. Everything is read from the file
// system and /proc, so it needs no privileges and no running sentientd.
type CheckOptions struct {
	PolicyFile string   // the installed policy
	Homes      []string // whose agent installs the policy should name
	Proc       string   // the /proc directory ("/proc" when empty)
}

// Line is one line of a check report.
type Line struct {
	Label string
	Text  string
	OK    bool
}

// Report is the result of Check.
type Report struct {
	Lines []Line
}

// OK is true when every line is.
func (r Report) OK() bool {
	for _, l := range r.Lines {
		if !l.OK {
			return false
		}
	}
	return true
}

// Check compares the installed policy with the agents installed now, and the
// agents that are running with the moment Tetragon started. Tetragon loads the
// policy when it starts, so an agent that started earlier has children that
// are not followed: its commands are recorded, the files and connections they
// touch are not.
func Check(o CheckOptions) Report {
	if o.PolicyFile == "" {
		o.PolicyFile = DefaultPolicyFile
	}
	if o.Proc == "" {
		o.Proc = "/proc"
	}
	var r Report
	add := func(label string, ok bool, format string, args ...any) {
		r.Lines = append(r.Lines, Line{Label: label, OK: ok, Text: fmt.Sprintf(format, args...)})
	}

	installed := Discover(o.Homes)
	if len(installed) == 0 {
		add("agent", false, "no agent found under %v", o.Homes)
		return r
	}

	text, err := os.ReadFile(o.PolicyFile)
	var policyTime time.Time
	switch {
	case err != nil:
		add("policy", false, "not installed (%s). Install it: sen policy | sudo tee %s", o.PolicyFile, o.PolicyFile)
	default:
		if fi, err := os.Stat(o.PolicyFile); err == nil {
			policyTime = fi.ModTime()
		}
		listed := PolicyBinaries(string(text))
		var missing []string
		for _, b := range installed {
			if !slices.Contains(listed, b) {
				missing = append(missing, b)
			}
		}
		if len(missing) > 0 {
			add("policy", false, "%s does not list %s. An agent update stops the policy matching. Rerun sen policy, install it, restart Tetragon, then restart the agent",
				o.PolicyFile, strings.Join(missing, ", "))
		} else {
			add("policy", true, "%s lists the %d agent binaries installed now", o.PolicyFile, len(installed))
		}
	}

	boot, err := bootTime(o.Proc)
	if err != nil {
		add("tetragon", false, "cannot read %s: %v", o.Proc, err)
		return r
	}
	procs := scan(o.Proc, boot)
	var tetragon *proc
	for i := range procs {
		if procs[i].comm == "tetragon" {
			tetragon = &procs[i]
			break
		}
	}
	if tetragon == nil {
		add("tetragon", false, "not running. Start it, then restart your agents")
		return r
	}
	if !policyTime.IsZero() && policyTime.After(tetragon.start) {
		add("tetragon", false, "running since %s, but the policy file changed after that. Restart Tetragon (it does not reload a changed policy)", stamp(tetragon.start))
	} else {
		add("tetragon", true, "running since %s, policy loaded then", stamp(tetragon.start))
	}

	roots := agentRoots(procs, installed)
	var early []proc
	for _, p := range roots {
		if p.start.Before(tetragon.start) {
			early = append(early, p)
		}
	}
	switch {
	case len(roots) == 0:
		add("agents", true, "none running")
	case len(early) == 0:
		add("agents", true, "%d running, all started after the policy loaded", len(roots))
	default:
		for _, p := range early {
			add("agents", false, "claude %s (pid %d) started %s, before the policy loaded. Its commands are recorded, but not the files and connections they touch. Restart it for full capture",
				filepath.Base(p.exe), p.pid, stamp(p.start))
		}
	}
	return r
}

func stamp(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") }

// PolicyBinaries returns the agent binaries a policy names in its
// matchBinaries values.
func PolicyBinaries(policy string) []string {
	var out []string
	in := false
	sc := bufio.NewScanner(strings.NewReader(policy))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "- matchBinaries:") || line == "matchBinaries:":
			in = true
		case strings.HasPrefix(line, "followChildren"):
			in = false
		case in && strings.HasPrefix(line, `- "`):
			if p, err := strconv.Unquote(strings.TrimPrefix(line, "- ")); err == nil && strings.HasPrefix(p, "/") {
				out = append(out, p)
			}
		}
	}
	return out
}

type proc struct {
	pid, ppid int
	comm, exe string
	start     time.Time
}

func bootTime(procDir string) (time.Time, error) {
	b, err := os.ReadFile(filepath.Join(procDir, "stat"))
	if err != nil {
		return time.Time{}, err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "btime "); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return time.Unix(n, 0), err
		}
	}
	return time.Time{}, fmt.Errorf("no btime in %s/stat", procDir)
}

// scan reads every process it is allowed to see. exe is empty for processes
// of other users.
func scan(procDir string, boot time.Time) []proc {
	es, _ := os.ReadDir(procDir)
	var out []proc
	for _, e := range es {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(procDir, e.Name(), "stat"))
		if err != nil {
			continue
		}
		s := string(b)
		open, closing := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
		if open < 0 || closing < open {
			continue
		}
		rest := strings.Fields(s[closing+1:])
		if len(rest) < 20 {
			continue
		}
		ppid, err1 := strconv.Atoi(rest[1])
		ticks, err2 := strconv.ParseInt(rest[19], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		exe, _ := os.Readlink(filepath.Join(procDir, e.Name(), "exe"))
		out = append(out, proc{
			pid: pid, ppid: ppid, comm: s[open+1 : closing],
			exe:   strings.TrimSuffix(exe, " (deleted)"),
			start: boot.Add(time.Duration(ticks) * time.Second / clockTicks),
		})
	}
	return out
}

// agentRoots are the running agent processes whose parent is not one too: the
// agent starts copies of itself as helpers, and those are not sessions.
func agentRoots(procs []proc, agents []string) []proc {
	isAgent := map[int]bool{}
	for _, p := range procs {
		if slices.Contains(agents, p.exe) {
			isAgent[p.pid] = true
		}
	}
	var out []proc
	for _, p := range procs {
		if isAgent[p.pid] && !isAgent[p.ppid] {
			out = append(out, p)
		}
	}
	return out
}

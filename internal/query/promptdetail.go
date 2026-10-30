package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/store"
)

// ErrNotFound means the requested prompt (or other record) doesn't exist.
var ErrNotFound = errors.New("not found")

// PromptDetail is everything the prompt page shows.
type PromptDetail struct {
	HistoryBegins time.Time
	Prompt        PromptRow
	// Activity holds exactly the rows of StatusRows, scoped to this prompt.
	Activity []ActivityRow
	Files    []FileChange
	// Steps is the trace: the prompt itself, then every action in order.
	Steps   []Step
	Reply   string // the agent's final message for the prompt
	ReplyAt time.Time
	// RepoRoot is the session's repo (empty if it has none), so views can
	// show repo-relative paths.
	RepoRoot string
	Counts   EvidenceCounts
}

// EvidenceCounts says how the two logs relate for one prompt. Shell builtins
// are left out of every number: the kernel can't see them.
type EvidenceCounts struct {
	// Watched: the kernel was recording while the prompt ran. Without it only
	// AgentLog means anything.
	//
	// The numbers are meant to add up, and tests check that they do:
	//   AgentLog  = Confirmed + Unverified + NotChecked + Pending
	//   KernelLog = Confirmed + Unreported
	Watched    bool
	AgentLog   int64 // actions the agent logged (shell builtins left out)
	KernelLog  int64 // what the kernel saw that is not background: confirmed + unreported
	Confirmed  int64 // logged and seen by the kernel
	Unverified int64 // logged commands the kernel did not see
	NotChecked int64 // logged actions that are not commands (file tools, web): the kernel cannot be matched to them yet
	Pending    int64 // logged commands of a prompt still running, not matched yet
	Unreported int64 // seen by the kernel, in nobody's log, not a known background command
	// Background is how many background commands the agent ran while the
	// prompt did; they are not part of the prompt (see AgentID).
	Background int64
	AgentID    string // the agent process that ran them ("" when none did)
}

// DiffLine is one line of a file diff. Kind is "h" (hunk or note), "a"
// (added), "d" (removed) or "c" (context).
type DiffLine struct {
	Kind, Text string
}

// FileChange is one file the prompt changed, with its edits merged.
type FileChange struct {
	Path           string
	Action         string // create | update | delete (the first edit's)
	Added, Removed int64
	Outside        bool    // outside the session's repo
	Steps          []int64 // step numbers of the edits
	By             string  // tool that made the change
	First          time.Time
	Diff           []DiffLine
}

// Step is one row of the trace.
type Step struct {
	N                int64
	Offset           time.Duration // since the prompt started
	Resource, Action string
	Path             string // the file, command, host…
	Failed           bool
	Evidence         string
	// A Bash tool call that ran several commands: the same CallID on each
	// of its steps, CallCommand (the whole line) on the first.
	CallID, CallCommand string
	Builtin             bool  // a shell builtin: it never execs, so only the log saw it
	ActionID            int64 // the action row behind this step (0 for the prompt itself); alerts point at it
	Alert               bool  // an alert is about this step
	Unreported          bool  // the kernel saw it, the agent's log does not have it
	Unverified          bool  // the agent logged it, the kernel did not see it
}

// maxDiffLines caps a file's diff; the rest is summarized.
const maxDiffLines = 200

// builtins are the bash builtins: the shell runs them itself, so there is no
// process and the kernel never sees them. That includes the ones that also
// exist as programs (echo, printf, test, kill, pwd, true, false): typed as a bare
// word, bash runs its own copy. Only an explicit path (/bin/echo) or `env` runs
// the program, and then the first word is not the bare name. `exec` is not
// here: it replaces the shell with a program, which the kernel does see.
var builtins = map[string]bool{
	"cd": true, "export": true, "source": true, ".": true, "alias": true, "unalias": true,
	"unset": true, "set": true, "pushd": true, "popd": true, "dirs": true, "ulimit": true, "umask": true,
	"exit": true, "return": true, "shift": true, "eval": true, "trap": true, "wait": true,
	"local": true, "declare": true, "typeset": true, "readonly": true, "type": true, "hash": true,
	"echo": true, "printf": true, "test": true, "[": true, "[[": true, "pwd": true, "true": true,
	"false": true, ":": true, "kill": true, "read": true, "builtin": true,
	"let": true, "getopts": true, "break": true, "continue": true, "jobs": true, "bg": true,
	"fg": true, "disown": true, "history": true, "fc": true, "times": true, "enable": true,
	"help": true, "bind": true, "shopt": true, "compgen": true, "complete": true, "mapfile": true,
	"readarray": true, "caller": true, "suspend": true, "logout": true,
}

var assignmentRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// isBuiltin says whether the command a part runs is a shell builtin. Leading
// VAR=value assignments belong to the command, so `A=1 echo x` is still echo.
// (`command ls` runs a program, so `command` is not a builtin here.)
func isBuiltin(command string) bool {
	f := strings.Fields(command)
	for len(f) > 0 && assignmentRE.MatchString(f[0]) {
		f = f[1:]
	}
	return len(f) > 0 && builtins[f[0]]
}

// GetPromptDetail returns the detail of prompt id, or ErrNotFound. now
// decides whether a prompt still counts as running.
func GetPromptDetail(ctx context.Context, db *sql.DB, id int64, now time.Time) (*PromptDetail, error) {
	d := &PromptDetail{}
	var err error
	if d.HistoryBegins, err = HistoryBegins(ctx, db); err != nil {
		return nil, err
	}

	var (
		session  int64
		seq      int64
		start    string
		endAt    sql.NullString
		dir      string
		repoRoot string
	)
	p := &d.Prompt
	err = db.QueryRowContext(ctx, `
SELECT p.id, p.session_id, p.seq, p.text, p.started_at, p.ended_at, s.agent, COALESCE(s.repo_root, s.cwd, ''), COALESCE(s.repo_root, ''), s.agent_session_id
FROM prompts p JOIN sessions s ON s.id = p.session_id WHERE p.id = ?`, id).
		Scan(&p.ID, &session, &seq, &p.Text, &start, &endAt, &p.Agent, &dir, &repoRoot, &p.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("prompt: %w", err)
	}
	if dir != "" {
		p.Repo = filepath.Base(dir)
	}
	d.RepoRoot = repoRoot
	if err := finishPrompt(ctx, db, p, start, endAt, now); err != nil {
		return nil, err
	}

	if d.Activity, err = promptActivity(ctx, db, p, repoRoot != ""); err != nil {
		return nil, err
	}
	if d.Counts.Watched, err = kernelWatched(ctx, db, p, now); err != nil {
		return nil, err
	}
	if d.Steps, d.Files, d.Counts, err = promptSteps(ctx, db, p, repoRoot, d.Counts); err != nil {
		return nil, err
	}
	if d.Counts.Watched {
		if d.Counts.Background, d.Counts.AgentID, err = backgroundDuring(ctx, db, session, p, now); err != nil {
			return nil, err
		}
	}
	if d.Reply, d.ReplyAt, err = promptReply(ctx, db, session, seq, start); err != nil {
		return nil, err
	}
	return d, nil
}

// promptActivity is the 11 summary rows scoped to the prompt.
func promptActivity(ctx context.Context, db *sql.DB, p *PromptRow, hasRepo bool) ([]ActivityRow, error) {
	rows := map[string]*ActivityRow{}
	for _, r := range StatusRows {
		rows[r[0]+" "+r[1]] = &ActivityRow{Resource: r[0], Action: r[1]}
	}
	pr := rows["prompt request"]
	pr.Count = 1
	if hasRepo || p.Repo != "" {
		pr.Repos = 1
		pr.Summary = "1 repo"
	}
	if err := actionRows(ctx, db, `a.prompt_id = ?`, []any{p.ID}, rows); err != nil {
		return nil, err
	}
	out := make([]ActivityRow, 0, len(StatusRows))
	for _, r := range StatusRows {
		out = append(out, *rows[r[0]+" "+r[1]])
	}
	return out, nil
}

// machineAction says whether a logged action is something done on this
// machine, so the kernel can in principle have seen it. A web search runs
// server-side and a subagent start is the agent's own bookkeeping.
func machineAction(resource, action string) bool {
	switch resource {
	case "command", "file", "dir", "process", "network", "git", "launch item":
		return true
	case "web":
		return action == "fetch"
	}
	return false
}

// kernelMatchable says whether the merge can pair a logged action with kernel
// events: commands by their exec, file reads and writes by the agent process's
// file events. The rest (a web fetch's connection, a file delete, ...) cannot
// be compared yet.
func kernelMatchable(resource, action string) bool {
	switch resource {
	case "command":
		return true
	case "file":
		return action == "read" || action == "update" || action == "create"
	}
	return false
}

// promptEnd is when the prompt's activity stopped, or now while it runs.
func promptEnd(p *PromptRow, now time.Time) time.Time {
	if p.Running {
		return now
	}
	return p.Started.Add(p.Duration)
}

// kernelWatched says whether the kernel recorded anything while the prompt ran.
func kernelWatched(ctx context.Context, db *sql.DB, p *PromptRow, now time.Time) (bool, error) {
	var seen int
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM kernel_events WHERE ts >= ? AND ts <= ?)`,
		dbtime.Format(p.Started), dbtime.Format(promptEnd(p, now))).Scan(&seen)
	if err != nil {
		return false, fmt.Errorf("kernel watched: %w", err)
	}
	return seen != 0, nil
}

// backgroundDuring counts the background commands the session's agent ran
// while the prompt did, and names the agent process that ran most of them.
func backgroundDuring(ctx context.Context, db *sql.DB, session int64, p *PromptRow, now time.Time) (int64, string, error) {
	var n int64
	var agent sql.NullString
	err := db.QueryRowContext(ctx, `
SELECT COUNT(*), (SELECT a2.root_exec_id FROM process_attribution a2 JOIN processes p2 ON p2.exec_id = a2.exec_id
                  WHERE a2.session_id = ? AND COALESCE(a2.background, '') <> '' AND p2.started_at >= ? AND p2.started_at <= ?
                  GROUP BY a2.root_exec_id ORDER BY COUNT(*) DESC LIMIT 1)
FROM process_attribution a JOIN processes p ON p.exec_id = a.exec_id
WHERE a.session_id = ? AND COALESCE(a.background, '') <> '' AND p.started_at >= ? AND p.started_at <= ?`,
		session, dbtime.Format(p.Started), dbtime.Format(promptEnd(p, now)),
		session, dbtime.Format(p.Started), dbtime.Format(promptEnd(p, now))).Scan(&n, &agent)
	if err != nil {
		return 0, "", fmt.Errorf("background commands: %w", err)
	}
	return n, agent.String, nil
}

// promptSteps builds the trace, the files-changed list and the evidence
// counts from the prompt's actions.
func promptSteps(ctx context.Context, db *sql.DB, p *PromptRow, repoRoot string, counts EvidenceCounts) ([]Step, []FileChange, EvidenceCounts, error) {
	r, err := db.QueryContext(ctx, `
SELECT a.ts, a.resource, a.action, COALESCE(a.path, ''), a.failed, a.evidence, COALESCE(a.tool_use_id, ''), a.part,
       COALESCE(a.raw_record_id, 0), a.lines_added, a.lines_removed, a.id,
       EXISTS(SELECT 1 FROM alerts al WHERE al.action_id = a.id)
FROM actions a WHERE a.prompt_id = ? ORDER BY a.ts, a.id`, p.ID)
	if err != nil {
		return nil, nil, counts, fmt.Errorf("steps: %w", err)
	}
	type act struct {
		ts                     time.Time
		resource, action, path string
		failed, alert          bool
		evidence, toolUse      string
		part                   int
		rawID, added, removed  int64
		id                     int64
	}
	var acts []act
	parts := map[string]int{} // tool_use_id -> number of parts
	for r.Next() {
		var a act
		var ts string
		var failed, alert int
		if err := r.Scan(&ts, &a.resource, &a.action, &a.path, &failed, &a.evidence, &a.toolUse, &a.part, &a.rawID, &a.added, &a.removed, &a.id, &alert); err != nil {
			r.Close()
			return nil, nil, counts, err
		}
		if a.ts, err = dbtime.Parse(ts); err != nil {
			r.Close()
			return nil, nil, counts, err
		}
		a.failed, a.alert = failed != 0, alert != 0
		if a.toolUse != "" {
			parts[a.toolUse]++
		}
		acts = append(acts, a)
	}
	r.Close()
	if err := r.Err(); err != nil {
		return nil, nil, counts, err
	}

	_, line := PromptLine(p.Text)
	steps := []Step{{N: 1, Resource: "prompt", Action: "request", Path: Ellipsis(line, 120), Evidence: "log"}}
	next := int64(2) // the number of the next numbered step
	files := map[string]*FileChange{}
	var fileOrder []string
	seenCall := map[string]bool{}
	for _, a := range acts {
		offset := a.ts.Sub(p.Started)
		if offset < 0 {
			offset = 0
		}
		s := Step{
			N: next, Offset: offset,
			Resource: a.resource, Action: a.action, Path: a.path, Failed: a.failed, Evidence: a.evidence, ActionID: a.id,
			Alert: a.alert,
		}
		next++
		if a.resource == "command" && a.toolUse != "" && parts[a.toolUse] > 1 {
			s.CallID = a.toolUse
			if !seenCall[a.toolUse] {
				seenCall[a.toolUse] = true
				s.CallCommand = rawCommand(ctx, db, a.rawID, a.toolUse)
			}
		}
		if a.resource == "command" {
			s.Builtin = isBuiltin(a.path)
		}
		switch a.evidence {
		case "kernel":
			s.Unreported = true
			counts.Unreported++
			counts.KernelLog++
		case "both":
			counts.AgentLog++
			counts.KernelLog++
			counts.Confirmed++
		default: // log
			// builtins have no process, and a web search or a subagent start is not
			// something done on this machine: neither can be in the kernel's record
			if !s.Builtin && machineAction(a.resource, a.action) {
				counts.AgentLog++
				switch {
				case !kernelMatchable(a.resource, a.action), !counts.Watched:
					// no way to compare it with the kernel (yet), or nothing was recording
					counts.NotChecked++
				case p.Running:
					counts.Pending++ // may yet be matched
				default:
					s.Unverified = true
					counts.Unverified++
				}
			}
		}
		steps = append(steps, s)

		if a.resource != "file" || (a.action != "create" && a.action != "update" && a.action != "delete") || a.path == "" {
			continue
		}
		f := files[a.path]
		if f == nil {
			f = &FileChange{
				Path: a.path, Action: a.action, First: a.ts,
				Outside: repoRoot != "" && !within(a.path, repoRoot),
			}
			files[a.path] = f
			fileOrder = append(fileOrder, a.path)
		}
		f.Added += a.added
		f.Removed += a.removed
		f.Steps = append(f.Steps, s.N)
		d, by := editDiff(ctx, db, a.rawID, a.toolUse, len(f.Steps))
		if f.By == "" {
			f.By = by
		}
		f.Diff = append(f.Diff, d...)
	}
	out := make([]FileChange, 0, len(fileOrder))
	for _, path := range fileOrder {
		f := files[path]
		f.Diff = capDiff(f.Diff)
		out = append(out, *f)
	}
	return steps, out, counts, nil
}

// rawBlock is a content block of a stored agent record.
type rawBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type rawRecord struct {
	IsSidechain bool `json:"isSidechain"`
	Message     *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func (r *rawRecord) blocks() []rawBlock {
	if r.Message == nil || len(r.Message.Content) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(r.Message.Content, &s) == nil {
		return []rawBlock{{Type: "text", Text: s}}
	}
	var bs []rawBlock
	if json.Unmarshal(r.Message.Content, &bs) != nil {
		return nil
	}
	return bs
}

// toolBlock loads the stored record and returns its tool_use block with
// the given id.
func toolBlock(ctx context.Context, db *sql.DB, rawID int64, toolUse string) (rawBlock, bool) {
	if rawID == 0 || toolUse == "" {
		return rawBlock{}, false
	}
	var stored any
	if err := db.QueryRowContext(ctx, `SELECT record FROM raw_log_records WHERE id = ?`, rawID).Scan(&stored); err != nil {
		return rawBlock{}, false
	}
	rec, err := store.DecodeRecord(stored)
	if err != nil {
		return rawBlock{}, false
	}
	var r rawRecord
	if json.Unmarshal([]byte(rec), &r) != nil {
		return rawBlock{}, false
	}
	for _, b := range r.blocks() {
		if b.Type == "tool_use" && b.ID == toolUse {
			return b, true
		}
	}
	return rawBlock{}, false
}

// rawCommand returns the whole command line of a Bash tool call.
func rawCommand(ctx context.Context, db *sql.DB, rawID int64, toolUse string) string {
	b, ok := toolBlock(ctx, db, rawID, toolUse)
	if !ok {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(b.Input, &in)
	return strings.TrimSpace(in.Command)
}

// editDiff builds the diff of one file edit from the agent's own tool call
// (Edit/MultiEdit show old and new text; a Write shows the new contents).
// Until kernel capture saves file versions this is what the log claims.
// n is the edit's number for this file.
func editDiff(ctx context.Context, db *sql.DB, rawID int64, toolUse string, n int) ([]DiffLine, string) {
	b, ok := toolBlock(ctx, db, rawID, toolUse)
	if !ok {
		return []DiffLine{{"h", "the log doesn't hold this edit's text"}}, ""
	}
	var in struct {
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
		Content   string `json:"content"`
		Edits     []struct {
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		} `json:"edits"`
	}
	_ = json.Unmarshal(b.Input, &in)
	head := func(s string) DiffLine {
		if n > 1 {
			s = fmt.Sprintf("edit %d: %s", n, s)
		}
		return DiffLine{"h", s}
	}
	switch b.Name {
	case "Edit":
		return append([]DiffLine{head("Edit")}, replaceLines(in.OldString, in.NewString)...), "Edit"
	case "MultiEdit":
		out := []DiffLine{head(fmt.Sprintf("MultiEdit (%d changes)", len(in.Edits)))}
		for _, e := range in.Edits {
			out = append(out, replaceLines(e.OldString, e.NewString)...)
		}
		return out, "MultiEdit"
	case "Write":
		out := []DiffLine{head("Write: whole file")}
		for _, l := range splitLines(in.Content) {
			out = append(out, DiffLine{"a", l})
		}
		return out, "Write"
	}
	return []DiffLine{head(b.Name)}, b.Name
}

func replaceLines(oldText, newText string) []DiffLine {
	var out []DiffLine
	for _, l := range splitLines(oldText) {
		out = append(out, DiffLine{"d", l})
	}
	for _, l := range splitLines(newText) {
		out = append(out, DiffLine{"a", l})
	}
	return out
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func capDiff(d []DiffLine) []DiffLine {
	if len(d) <= maxDiffLines {
		return d
	}
	out := append([]DiffLine(nil), d[:maxDiffLines]...)
	return append(out, DiffLine{"h", fmt.Sprintf("… %d more lines", len(d)-maxDiffLines)})
}

// promptReply finds the agent's last text message for the prompt: the
// newest assistant record, before the session's next prompt, that has text.
func promptReply(ctx context.Context, db *sql.DB, session, seq int64, start string) (string, time.Time, error) {
	end := "9999"
	var next sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT MIN(started_at) FROM prompts WHERE session_id = ? AND seq > ?`, session, seq).Scan(&next); err != nil {
		return "", time.Time{}, fmt.Errorf("next prompt: %w", err)
	}
	if next.Valid {
		end = next.String
	}
	r, err := db.QueryContext(ctx, `
SELECT ts, record FROM raw_log_records
WHERE session_id = ? AND type = 'assistant' AND ts >= ? AND ts < ?
ORDER BY ts DESC, id DESC LIMIT 50`, session, start, end)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("reply: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var ts string
		var stored any
		if err := r.Scan(&ts, &stored); err != nil {
			return "", time.Time{}, err
		}
		rec, err := store.DecodeRecord(stored)
		if err != nil {
			continue
		}
		var rr rawRecord
		if json.Unmarshal([]byte(rec), &rr) != nil || rr.IsSidechain {
			continue
		}
		var texts []string
		for _, b := range rr.blocks() {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				texts = append(texts, strings.TrimSpace(b.Text))
			}
		}
		if len(texts) == 0 {
			continue
		}
		at, err := dbtime.Parse(ts)
		if err != nil {
			return "", time.Time{}, err
		}
		return strings.Join(texts, "\n\n"), at, nil
	}
	return "", time.Time{}, r.Err()
}

package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
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
}

// maxDiffLines caps a file's diff; the rest is summarized.
const maxDiffLines = 200

// builtins are shell builtins that never exec (so the kernel never sees
// them). Commands that exist both ways (echo, printf, test, kill) are left
// out: the kernel may well see those.
var builtins = map[string]bool{
	"cd": true, "export": true, "source": true, ".": true, "alias": true, "unalias": true,
	"unset": true, "set": true, "pushd": true, "popd": true, "ulimit": true, "umask": true,
	"exit": true, "return": true, "shift": true, "eval": true, "trap": true, "wait": true,
	"local": true, "declare": true, "readonly": true, "type": true, "hash": true,
}

func isBuiltin(command string) bool {
	f := strings.Fields(command)
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
	if d.Steps, d.Files, err = promptSteps(ctx, db, p, repoRoot); err != nil {
		return nil, err
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

// promptSteps builds the trace and the files-changed list from the
// prompt's actions.
func promptSteps(ctx context.Context, db *sql.DB, p *PromptRow, repoRoot string) ([]Step, []FileChange, error) {
	r, err := db.QueryContext(ctx, `
SELECT a.ts, a.resource, a.action, COALESCE(a.path, ''), a.failed, a.evidence, COALESCE(a.tool_use_id, ''), a.part,
       COALESCE(a.raw_record_id, 0), a.lines_added, a.lines_removed, a.id
FROM actions a WHERE a.prompt_id = ? ORDER BY a.ts, a.id`, p.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("steps: %w", err)
	}
	type act struct {
		ts                     time.Time
		resource, action, path string
		failed                 bool
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
		var failed int
		if err := r.Scan(&ts, &a.resource, &a.action, &a.path, &failed, &a.evidence, &a.toolUse, &a.part, &a.rawID, &a.added, &a.removed, &a.id); err != nil {
			r.Close()
			return nil, nil, err
		}
		if a.ts, err = dbtime.Parse(ts); err != nil {
			r.Close()
			return nil, nil, err
		}
		a.failed = failed != 0
		if a.toolUse != "" {
			parts[a.toolUse]++
		}
		acts = append(acts, a)
	}
	r.Close()
	if err := r.Err(); err != nil {
		return nil, nil, err
	}

	_, line := PromptLine(p.Text)
	steps := []Step{{N: 1, Resource: "prompt", Action: "request", Path: Ellipsis(line, 120), Evidence: "log"}}
	files := map[string]*FileChange{}
	var fileOrder []string
	seenCall := map[string]bool{}
	for _, a := range acts {
		s := Step{
			N: int64(len(steps) + 1), Offset: a.ts.Sub(p.Started),
			Resource: a.resource, Action: a.action, Path: a.path, Failed: a.failed, Evidence: a.evidence, ActionID: a.id,
		}
		if s.Offset < 0 {
			s.Offset = 0
		}
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
	return steps, out, nil
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
	var rec string
	if err := db.QueryRowContext(ctx, `SELECT record FROM raw_log_records WHERE id = ?`, rawID).Scan(&rec); err != nil {
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
		var ts, rec string
		if err := r.Scan(&ts, &rec); err != nil {
			return "", time.Time{}, err
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

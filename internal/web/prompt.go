package web

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/query"
	"github.com/nosaka4i/sentient/internal/render"
	"github.com/nosaka4i/sentient/internal/version"
)

// maxTraceSteps bounds the trace on the page; a prompt can run thousands of
// steps and the rest would only make the page heavy.
const maxTraceSteps = 2000

type promptPage struct {
	page
	Head                     promptHead
	Tiles                    []tile
	Evidence                 evidenceView
	Actions                  int64
	Files                    []fileView
	FilesAdded, FilesRemoved int64
	Steps                    []stepView
	StepCount                int
	TraceMore                int // steps not shown
	Reply                    []template.HTML
	ReplyTime                string
	ReplyLong                bool
}

type promptHead struct {
	Line         string // first line of the prompt
	Pasted       int
	Full         string // the whole prompt, shown when it's more than the line
	ISO, Date    string
	End          string // clock time of the last activity
	Agent, Repo  string
	SessionHref  string
	SessionShort string
	Duration     string
	Actions      int64
	Running      bool
}

type diffView struct {
	Class, Prefix, Text string
}

type fileView struct {
	Path           string
	Added, Removed int64
	Outside        bool
	Open           bool
	StepsLabel     string // "step 14" / "steps 14, 22"
	Verb           string // "update"
	By             string // "Edit tool"
	Clock          string
	Diff           []diffView
}

type stepView struct {
	N        string
	T        string // +m:ss
	Res, Act string
	Path     string
	Note     string
	Failed   bool
	Flagged  bool // an alert is about this step
	Unrep    bool // the kernel saw it, the agent's log does not have it
	Call     bool // a Bash call header: Path holds the whole command line
	CallNote string
	Children []stepView
	Prompt   bool
	// Group: every run of one background command, as one row.
	Group    bool
	Runs     []runView
	RunsMore int
	// Pause: a band around the actions logged while nobody was recording the
	// kernel. Children holds them as the trace draws them; the band says when
	// the pause was, how many actions are in it and what kind.
	Pause     bool
	PauseWhen string // "Oct 7 14:47:05 – 14:47:25 (20s)"; empty when the gap is not known
	PauseNote string // "2 actions here are in the agent's log but were not compared with the kernel"
	PauseKind string // "62 commands · 31 file reads · 7 edits · steps 41–140"
	PauseOpen bool   // short pauses start open; a long one is collapsed
	PauseAt   string // +m:ss of its first action
	first     time.Duration
}

// pauseOpenMax is how many actions a pause band may hold and still start open.
const pauseOpenMax = 10

type runView struct {
	T, Path string
}

// evidenceView is the agent log / kernel log tiles above the trace. A count
// of what the kernel saw means nothing when it was not recording, so then only
// the agent log is shown.
type evidenceView struct {
	Watched                bool
	AgentLog, KernelLog    int64
	Unverified, Unreported int64
	// the rest of the agent log, so it adds up: confirmed + unverified +
	// not checked (file tools, web) + pending (a running prompt)
	NotChecked, Pending int64
	// logged while nobody was recording the kernel (a gap): no record to compare
	// with, so neither confirmed nor unverified
	NotCompared int64
	// Background commands the agent ran while the prompt did, and the agent's
	// timeline for the prompt's window.
	Background     int64
	BackgroundHref string
}

func (s *Server) prompt(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "no such prompt", http.StatusNotFound)
		return
	}
	d, err := s.src.PromptDetail(r.Context(), id)
	if errors.Is(err, api.ErrNotFound) || (err == nil && d == nil) {
		http.Error(w, "no such prompt", http.StatusNotFound)
		return
	}
	if err != nil {
		s.down(w, err)
		return
	}
	now := s.now()
	p := newPromptPage(d, now, s.home)
	s.render(w, http.StatusOK, "prompt", p)
}

func newPromptPage(d *query.PromptDetail, now time.Time, home string) promptPage {
	pr := newPromptView(d.Prompt, now)
	_, line := query.PromptLine(d.Prompt.Text)
	head := promptHead{
		Line: line, Pasted: pr.Pasted, ISO: pr.ISO, Date: pr.Date,
		Agent: pr.Agent, Repo: pr.Repo, SessionHref: pr.SessionHref, SessionShort: pr.SessionShort,
		Duration: pr.Duration, Actions: d.Prompt.Actions, Running: d.Prompt.Running,
	}
	if d.Prompt.Duration > 0 {
		head.End = d.Prompt.Started.Add(d.Prompt.Duration).In(now.Location()).Format("15:04")
	}
	if strings.TrimSpace(d.Prompt.Text) != line {
		head.Full = d.Prompt.Text
	}

	p := promptPage{
		page: page{Title: "prompt", Version: version.Version, Sub: true},
		Head: head, Actions: d.Prompt.Actions,
	}
	if !d.HistoryBegins.IsZero() {
		p.Since = &since{
			ISO:  d.HistoryBegins.In(now.Location()).Format("2006-01-02T15:04"),
			Date: render.Date(d.HistoryBegins, now),
			Ago:  render.Ago(d.HistoryBegins, now),
		}
	}

	p.Evidence = evidenceView{
		Watched: d.Counts.Watched, AgentLog: d.Counts.AgentLog, KernelLog: d.Counts.KernelLog,
		Unverified: d.Counts.Unverified, Unreported: d.Counts.Unreported, Background: d.Counts.Background,
		NotChecked: d.Counts.NotChecked, Pending: d.Counts.Pending, NotCompared: d.Counts.NotCompared,
	}
	if d.Counts.Background > 0 && d.Counts.AgentID != "" {
		end := now
		if !d.Prompt.Running {
			end = d.Prompt.Started.Add(d.Prompt.Duration)
		}
		end = end.Add(time.Second) // the link's times have no fraction of a second
		v := url.Values{"view": {"timeline"}, "from": {d.Prompt.Started.UTC().Format(time.RFC3339)}, "to": {end.UTC().Format(time.RFC3339)}}
		p.Evidence.BackgroundHref = "/agent/" + url.PathEscape(d.Counts.AgentID) + "?" + v.Encode()
	}
	p.Tiles = tiles(d.Activity)
	for i := range p.Tiles {
		p.Tiles[i].Href = "#trace"
		if t := p.Tiles[i]; t.Resource == "file" && (t.Action == "create" || t.Action == "update" || t.Action == "delete") {
			p.Tiles[i].Href = "#files" // the changes are in the files drawer
		}
		if p.Tiles[i].SubHref != "" {
			p.Tiles[i].SubHref = "#trace"
		}
		p.Tiles[i].Zero = p.Tiles[i].Count == 0
	}

	for i, f := range d.Files {
		p.Files = append(p.Files, newFileView(f, i == 0, d.RepoRoot, home, now))
		p.FilesAdded += f.Added
		p.FilesRemoved += f.Removed
	}
	p.Steps, p.StepCount, p.TraceMore = buildTrace(d.Steps)
	labelPauses(p.Steps, d.Gaps, d.Prompt.Started, now)

	if strings.TrimSpace(d.Reply) != "" {
		p.Reply = replyParagraphs(d.Reply)
		p.ReplyLong = len(d.Reply) > 240 || strings.Contains(d.Reply, "\n")
		if !d.ReplyAt.IsZero() {
			p.ReplyTime = d.ReplyAt.In(now.Location()).Format("15:04")
		}
	}
	return p
}

// DisplayPath shows a path repo-relative when it's inside the repo, ~/…
// when it's under the home directory, else as is.
func DisplayPath(path, repoRoot, home string) string {
	if repoRoot != "" {
		if rel, err := filepath.Rel(repoRoot, path); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return rel
		}
	}
	if home != "" {
		if rel, err := filepath.Rel(home, path); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return "~/" + rel
		}
	}
	return path
}

func newFileView(f query.FileChange, open bool, repoRoot, home string, now time.Time) fileView {
	v := fileView{
		Path: DisplayPath(f.Path, repoRoot, home), Added: f.Added, Removed: f.Removed, Outside: f.Outside,
		Open: open, Verb: f.Action, Clock: f.First.In(now.Location()).Format("15:04"),
	}
	if f.By != "" {
		v.By = f.By + " tool"
	}
	nums := make([]string, len(f.Steps))
	for i, n := range f.Steps {
		nums[i] = strconv.FormatInt(n, 10)
	}
	switch len(nums) {
	case 0:
	case 1:
		v.StepsLabel = "step " + nums[0]
	default:
		v.StepsLabel = "steps " + strings.Join(nums, ", ")
	}
	for _, l := range f.Diff {
		d := diffView{Text: l.Text}
		switch l.Kind {
		case "a":
			d.Class, d.Prefix = "a", "+"
		case "d":
			d.Class, d.Prefix = "d", "-"
		case "h":
			d.Class, d.Prefix = "h", "@@ "
			d.Text = l.Text + " @@"
		default:
			d.Prefix = " "
		}
		v.Diff = append(v.Diff, d)
	}
	return v
}

// offsetLabel formats a step's time since the prompt started: +m:ss.
func offsetLabel(d time.Duration) string {
	s := int(d / time.Second)
	return fmt.Sprintf("+%d:%02d", s/60, s%60)
}

// buildTrace turns the flat steps into the trace tree: a Bash call that ran
// several commands becomes a header with its commands nested under it.
// It returns the rows, how many steps they cover, and how many were cut.
func buildTrace(steps []query.Step) ([]stepView, int, int) {
	more := 0
	if len(steps) > maxTraceSteps {
		more = len(steps) - maxTraceSteps
		steps = steps[:maxTraceSteps]
	}
	// a run of steps logged while nobody was recording is one band
	var out []stepView
	for i := 0; i < len(steps); {
		j := i
		for j < len(steps) && steps[j].NotCompared == steps[i].NotCompared {
			j++
		}
		if steps[i].NotCompared {
			out = append(out, pauseBand(steps[i:j]))
		} else {
			out = append(out, traceRows(steps[i:j])...)
		}
		i = j
	}
	return out, len(steps), more
}

// traceRows draws steps one by one, a Bash call that ran several commands as a
// header with its commands under it.
func traceRows(steps []query.Step) []stepView {
	var out []stepView
	for i := 0; i < len(steps); i++ {
		s := steps[i]
		if s.CallID == "" {
			out = append(out, newStepView(s, true))
			continue
		}
		g := stepView{Call: true, T: offsetLabel(s.Offset), Path: s.CallCommand}
		for ; i < len(steps) && steps[i].CallID == s.CallID; i++ {
			g.Children = append(g.Children, newStepView(steps[i], false))
		}
		i-- // the loop's own increment steps past the group
		g.CallNote = fmt.Sprintf("1 tool call, %d commands", len(g.Children))
		out = append(out, g)
	}
	return out
}

// pauseBand is one band around steps the agent logged while nobody was
// recording the kernel. Nothing in it is hidden: the rows are the ones the
// trace draws anywhere else, and the band says what is inside by kind.
func pauseBand(steps []query.Step) stepView {
	b := stepView{Pause: true, PauseOpen: len(steps) <= pauseOpenMax, first: steps[0].Offset,
		PauseAt: offsetLabel(steps[0].Offset), Children: traceRows(steps)}
	var commands, reads, edits, other int
	for _, s := range steps {
		switch {
		case s.Resource == "command":
			commands++
		case s.Resource == "file" && s.Action == "read":
			reads++
		case s.Resource == "file":
			edits++
		default:
			other++
		}
	}
	var kinds []string
	for _, k := range []struct {
		n    int
		word string
	}{{commands, "command"}, {reads, "file read"}, {edits, "edit"}, {other, "other"}} {
		if k.n > 0 {
			kinds = append(kinds, plural(int64(k.n), k.word))
		}
	}
	if last := steps[len(steps)-1].N; last != steps[0].N {
		kinds = append(kinds, fmt.Sprintf("steps %d–%d", steps[0].N, last))
	} else {
		kinds = append(kinds, fmt.Sprintf("step %d", steps[0].N))
	}
	if len(steps) == 1 {
		b.PauseNote = "1 action here is in the agent's log but was not compared with the kernel"
	} else {
		b.PauseNote = fmt.Sprintf("%d actions here are in the agent's log but were not compared with the kernel", len(steps))
	}
	b.PauseKind = strings.Join(kinds, " · ")
	return b
}

// labelPauses says when each band's pause was, from the gap that holds its
// first action (widened by the same few seconds the accounting allows).
func labelPauses(rows []stepView, gaps []query.GapInfo, started, now time.Time) {
	const slack = 5 * time.Second
	for i := range rows {
		if !rows[i].Pause {
			continue
		}
		at := started.Add(rows[i].first)
		for _, g := range gaps {
			if at.Before(g.From.Add(-slack)) || at.After(g.To.Add(slack)) {
				continue
			}
			end := g.To
			when := stamp(g.From, now) + " – " + clockOrDate(end, g.From, now)
			if g.Open {
				when = stamp(g.From, now) + " – still paused"
			} else {
				when += " (" + uptime(end.Sub(g.From)) + ")"
			}
			rows[i].PauseWhen = when
			break
		}
	}
}

func newStepView(s query.Step, top bool) stepView {
	v := stepView{
		N: strconv.FormatInt(s.N, 10), Res: s.Resource, Act: s.Action, Path: s.Path, Failed: s.Failed,
		Prompt: s.Resource == "prompt", Flagged: s.Alert, Unrep: s.Unreported,
	}
	if top {
		v.T = offsetLabel(s.Offset)
	}
	switch {
	case s.Builtin:
		v.Note = "shell builtin, never execs"
	case s.Unverified:
		v.Note = "not seen by the kernel"
	case s.Unreported:
		v.Note = "not in the agent's log"
	}
	return v
}

var codeSpanRE = regexp.MustCompile("`([^`\n]+)`")

// replyParagraphs splits the agent's message into escaped paragraphs, with
// `code` spans marked up.
func replyParagraphs(text string) []template.HTML {
	var out []template.HTML
	for _, para := range strings.Split(strings.TrimSpace(text), "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		esc := template.HTMLEscapeString(para)
		esc = codeSpanRE.ReplaceAllString(esc, "<code>$1</code>")
		out = append(out, template.HTML(esc))
	}
	return out
}

package web

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
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
	Flagged  bool // the step an alert is about (the alert page)
	Ev       string
	Call     bool // a Bash call header: Path holds the whole command line
	CallNote string
	Children []stepView
	Prompt   bool
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
	if d.Prompt.Running {
		p.Refresh = RefreshSeconds // a finished prompt doesn't change; don't reload it
		p.Live = true
	}
	if !d.HistoryBegins.IsZero() {
		p.Since = &since{
			ISO:  d.HistoryBegins.In(now.Location()).Format("2006-01-02T15:04"),
			Date: render.Date(d.HistoryBegins, now),
			Ago:  render.Ago(d.HistoryBegins, now),
		}
	}

	p.Tiles = tiles(d.Activity)
	for i := range p.Tiles {
		p.Tiles[i].Href = "#trace"
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
	var out []stepView
	for i := 0; i < len(steps); i++ {
		s := steps[i]
		if s.CallID == "" {
			out = append(out, newStepView(s, true))
			continue
		}
		g := stepView{Call: true, T: offsetLabel(s.Offset), Ev: "log", Path: s.CallCommand}
		for ; i < len(steps) && steps[i].CallID == s.CallID; i++ {
			g.Children = append(g.Children, newStepView(steps[i], false))
		}
		i-- // the loop's own increment steps past the group
		g.CallNote = fmt.Sprintf("1 tool call, %d commands", len(g.Children))
		if allBoth(g.Children) {
			g.Ev = "both"
		}
		out = append(out, g)
	}
	return out, len(steps), more
}

func allBoth(steps []stepView) bool {
	for _, s := range steps {
		if s.Ev != "both" {
			return false
		}
	}
	return len(steps) > 0
}

func newStepView(s query.Step, top bool) stepView {
	v := stepView{
		N: strconv.FormatInt(s.N, 10), Res: s.Resource, Act: s.Action, Path: s.Path, Failed: s.Failed, Ev: s.Evidence,
		Prompt: s.Resource == "prompt",
	}
	if top {
		v.T = offsetLabel(s.Offset)
	}
	if s.Builtin {
		v.Note = "shell builtin, never execs"
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

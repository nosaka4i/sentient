package web

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/query"
	"github.com/nosaka4i/sentient/internal/render"
	"github.com/nosaka4i/sentient/internal/version"
)

// sinceOf is the header's "History from" data (nil when nothing is recorded).
func sinceOf(t, now time.Time) *since {
	if t.IsZero() {
		return nil
	}
	return &since{
		ISO:  t.In(now.Location()).Format("2006-01-02T15:04"),
		Date: render.Date(t, now),
		Ago:  render.Ago(t, now),
	}
}

// ---- /session/<id> ----

type sessionPage struct {
	page
	Head      sessionHead
	Tiles     []tile
	Prompts   []sessionRow
	MoreHref  string // the prompts page for this session, when it has more than are listed
	Older     int64  // how many prompts that page has beyond the ones listed
	Subagents []subagentView
}

type sessionHead struct {
	Short, ID    string
	First        string // the session's first prompt, one line
	Pasted       int
	Agent, Repo  string
	Cwd          string
	ISO, Date    string
	End          string
	Duration     string
	PromptCount  int64
	Actions      int64
	Flags        int64
	Running      bool
	AgentVersion string
}

// sessionRow is a prompt in the session's own list, numbered from the
// session's first prompt.
type sessionRow struct {
	N int64
	promptView
	Time string
}

type subagentView struct {
	Kind, Task, Clock string
	PromptNumber      int64
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	d, err := s.src.SessionDetail(r.Context(), r.PathValue("id"))
	if errors.Is(err, api.ErrNotFound) || (err == nil && d == nil) {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	if err != nil {
		s.down(w, err)
		return
	}
	s.render(w, http.StatusOK, "session", newSessionPage(d, s.now(), s.home))
}

func newSessionPage(d *query.SessionDetail, now time.Time, home string) sessionPage {
	h := sessionHead{
		Short: query.ShortSession(d.ID), ID: d.ID, Agent: d.Agent, Repo: d.Repo, AgentVersion: d.AgentVersion,
		Cwd: DisplayPath(d.Cwd, "", home), PromptCount: d.TotalPrompts, Actions: d.Actions, Flags: d.Flags, Running: d.Running,
	}
	if !d.Started.IsZero() {
		h.ISO = d.Started.In(now.Location()).Format("2006-01-02T15:04")
		h.Date = render.Date(d.Started, now)
		if end := d.LastActivity; !end.IsZero() && end.After(d.Started) {
			h.End = end.In(now.Location()).Format("15:04")
			h.Duration = query.ShortDuration(end.Sub(d.Started))
		}
	}
	if d.FirstPrompt != "" {
		pasted, line := query.PromptLine(d.FirstPrompt)
		h.Pasted, h.First = pasted, query.Ellipsis(line, 160)
	}

	p := sessionPage{page: page{Title: "session " + h.Short, Version: version.Version, Sub: true}, Head: h}
	if d.Running {
		p.Refresh, p.Live = RefreshSeconds, true
	}
	p.Since = sinceOf(d.HistoryBegins, now)

	p.Tiles = tiles(d.Activity)
	for i := range p.Tiles {
		p.Tiles[i].Zero = p.Tiles[i].Count == 0
		p.Tiles[i].Href = "#prompts"
		if p.Tiles[i].SubHref != "" {
			p.Tiles[i].SubHref = "#prompts"
		}
		if p.Tiles[i].Resource == "subagent" {
			p.Tiles[i].Href = "#subagents"
		}
	}
	// d.Prompts holds the newest few, newest first; the first is the
	// session's TotalPrompts-th.
	for i, pr := range d.Prompts {
		v := newPromptView(pr, now)
		p.Prompts = append(p.Prompts, sessionRow{N: d.TotalPrompts - int64(i), promptView: v, Time: v.Date})
	}
	if d.TotalPrompts > int64(len(d.Prompts)) {
		p.MoreHref = "/prompts?" + url.Values{"session": {d.ID}}.Encode()
		p.Older = d.TotalPrompts - int64(len(d.Prompts))
	}
	for _, sa := range d.Subagents {
		p.Subagents = append(p.Subagents, subagentView{
			Kind: sa.Kind, Task: sa.Task, Clock: sa.At.In(now.Location()).Format("15:04"), PromptNumber: sa.PromptNumber,
		})
	}
	return p
}

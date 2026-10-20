package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/query"
	"github.com/nosaka4i/sentient/internal/render"
	"github.com/nosaka4i/sentient/internal/version"
)

// Page sizes of /prompts: the default view counts sessions, a single
// session counts prompts. "Show more" adds one step.
const (
	sessionStep = query.DefaultSessionLimit
	promptStep  = query.DefaultListLimit
)

// ---- /prompts ----

type promptsPage struct {
	page
	Query    string
	Flagged  bool
	Agents   []option
	Repos    []option
	Sessions []sessionOption
	Total    int64 // prompts matching the filters
	Shown    string
	// One is set when a single session is selected; Summary describes it and
	// Rows are its prompts. Otherwise Groups holds every session.
	One      bool
	Summary  sessionSummary
	Rows     []promptView
	Groups   []sessionGroup
	Filtered bool
	// FlaggedHref toggles "flagged only", keeping the other filters.
	FlaggedHref string
	Clear       string // drops every filter ("" when none is set)
	MoreHref    string
	MoreText    string
}

type option struct {
	Value    string
	Selected bool
}

// sessionOption is an entry of the Session menu.
type sessionOption struct {
	Value, Label       string
	Selected, Disabled bool
}

// sessionSummary is the one-line description of the selected session.
type sessionSummary struct {
	ID, Short, Repo, Range, Href string
	Prompts, Actions, Flags      int64
}

// sessionGroup is one session in the default view: a header bar and the
// newest of its matching prompts.
type sessionGroup struct {
	SessionHref, Short, ID string
	Agent, Repo            string
	Range                  string // "Oct 7 14:02–15:26"
	CountText              string // "128 prompts" or "5 of 128 prompts match"
	SeeAll                 string // link to the session's own view when rows are cut
	Rows                   []promptView
}

func (s *Server) prompts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := query.PromptFilter{
		Text:    clip(strings.TrimSpace(q.Get("q")), 200),
		Agent:   q.Get("agent"),
		Repo:    q.Get("repo"),
		Flagged: q.Get("flagged") == "1",
	}
	n, _ := strconv.Atoi(q.Get("n"))
	now := s.now()
	p := promptsPage{
		page:     page{Title: "prompts", Version: version.Version, Sub: true},
		Query:    f.Text,
		Flagged:  f.Flagged,
		Filtered: f.Text != "" || f.Agent != "" || f.Repo != "" || f.Flagged,
	}
	session := q.Get("session")

	if session != "" {
		one := f
		one.SessionID = session
		one.Limit = pageSize(n, promptStep, query.MaxListLimit)
		l, err := s.src.ListPrompts(r.Context(), one)
		if err != nil {
			s.down(w, err)
			return
		}
		// An unknown session, or one the agent/repo filters exclude, falls
		// back to the default view.
		if sum := l.Session; sum != nil && matchesFilters(sum.Agent, sum.Repo, f) {
			s.renderOne(w, p, l, f, now)
			return
		}
		session = ""
	}

	g := f
	g.Limit = pageSize(n, sessionStep, query.MaxSessionLimit)
	groups, err := s.src.ListSessionGroups(r.Context(), g, query.DefaultPerSession)
	if err != nil {
		s.down(w, err)
		return
	}
	s.renderAll(w, p, groups, f, now)
}

// pageSize is the requested size, or the default; capped.
func pageSize(n, def, max int) int {
	if n <= 0 {
		return def
	}
	return min(n, max)
}

func matchesFilters(agent, repo string, f query.PromptFilter) bool {
	return (f.Agent == "" || f.Agent == agent) && (f.Repo == "" || f.Repo == repo)
}

func (s *Server) renderOne(w http.ResponseWriter, p promptsPage, l *query.PromptList, f query.PromptFilter, now time.Time) {
	sum := l.Session
	p.One = true
	p.Total = l.Total
	p.Since = sinceOf(l.HistoryBegins, now)
	p.Agents, p.Repos = options(l.Agents, f.Agent), options(l.Repos, f.Repo)
	p.Sessions = sessionOptions(l.Sessions, l.OlderSessions, sum, f)
	p.Summary = sessionSummary{
		ID: sum.ID, Short: query.ShortSession(sum.ID), Repo: sum.Repo, Href: "/session/" + url.PathEscape(sum.ID),
		Range: timeRange(sum.Started, sum.LastActivity, now), Prompts: sum.Prompts, Actions: sum.Actions, Flags: sum.Flags,
	}
	for _, pr := range l.Prompts {
		p.Rows = append(p.Rows, newPromptView(pr, now))
	}
	p.Shown = fmt.Sprintf("%d of %d", len(l.Prompts), l.Total)
	if p.Filtered {
		p.Shown += " match"
	} else {
		p.Shown += " shown"
	}
	p.FlaggedHref = listHref(f, sum.ID, !f.Flagged, 0)
	if p.Filtered {
		p.Clear = listHref(query.PromptFilter{}, sum.ID, false, 0)
	}
	if rem := l.Total - int64(len(l.Prompts)); rem > 0 && len(l.Prompts) < query.MaxListLimit {
		p.MoreHref = listHref(f, sum.ID, f.Flagged, len(l.Prompts)+promptStep)
		p.MoreText = fmt.Sprintf("%d older prompts in this session", rem)
	}
	s.render(w, http.StatusOK, "prompts", p)
}

func (s *Server) renderAll(w http.ResponseWriter, p promptsPage, g *query.SessionGroups, f query.PromptFilter, now time.Time) {
	p.Total = g.TotalPrompts
	p.Since = sinceOf(g.HistoryBegins, now)
	p.Agents, p.Repos = options(g.Agents, f.Agent), options(g.Repos, f.Repo)
	p.Sessions = sessionOptions(g.Sessions, g.OlderSessions, nil, f)
	for _, grp := range g.Groups {
		sum := grp.Session
		sg := sessionGroup{
			SessionHref: "/session/" + url.PathEscape(sum.ID), Short: query.ShortSession(sum.ID), ID: sum.ID,
			Agent: sum.Agent, Repo: sum.Repo, Range: timeRange(sum.Started, sum.LastActivity, now),
			CountText: plural(sum.Prompts, "prompt"),
		}
		if p.Filtered {
			sg.CountText = fmt.Sprintf("%d of %s match", grp.Matching, plural(sum.Prompts, "prompt"))
		}
		if grp.Matching > int64(len(grp.Prompts)) {
			sg.SeeAll = listHref(f, sum.ID, f.Flagged, 0)
		}
		for _, pr := range grp.Prompts {
			sg.Rows = append(sg.Rows, newPromptView(pr, now))
		}
		p.Groups = append(p.Groups, sg)
	}
	p.Shown = fmt.Sprintf("%d of %s", len(g.Groups), plural(g.TotalSessions, "session"))
	if p.Filtered {
		p.Shown += fmt.Sprintf(" · %s match", plural(g.TotalPrompts, "prompt"))
	}
	p.FlaggedHref = listHref(f, "", !f.Flagged, 0)
	if p.Filtered {
		p.Clear = "/prompts"
	}
	if rem := g.TotalSessions - int64(len(g.Groups)); rem > 0 && len(g.Groups) < query.MaxSessionLimit {
		p.MoreHref = listHref(f, "", f.Flagged, len(g.Groups)+sessionStep)
		p.MoreText = plural(rem, "older session")
	}
	s.render(w, http.StatusOK, "prompts", p)
}

// sessionOptions builds the Session menu: the recent sessions (those the
// agent/repo filters exclude are disabled), the selected one even if it is
// older, and a note on how many are left out.
func sessionOptions(recent []query.SessionOption, older int64, selected *query.SessionSummary, f query.PromptFilter) []sessionOption {
	var out []sessionOption
	found := false
	for _, o := range recent {
		so := sessionOption{Value: o.ID, Label: sessionLabel(o.ID, o.Repo, o.Agent), Disabled: !matchesFilters(o.Agent, o.Repo, f)}
		if selected != nil && o.ID == selected.ID {
			so.Selected, found = true, true
		}
		out = append(out, so)
	}
	if selected != nil && !found {
		out = append(out, sessionOption{Value: selected.ID, Label: sessionLabel(selected.ID, selected.Repo, selected.Agent), Selected: true})
	}
	if older > 0 {
		out = append(out, sessionOption{Label: plural(older, "older session") + "…", Disabled: true})
	}
	return out
}

func sessionLabel(id, repo, agent string) string {
	short := query.ShortSession(id)
	if repo != "" {
		return short + " · " + repo
	}
	return short + " · " + agent
}

// listHref builds a /prompts link with the filters that are set. session
// "" is the default view; n is the page size (0 = default).
func listHref(f query.PromptFilter, session string, flagged bool, n int) string {
	v := url.Values{}
	if f.Text != "" {
		v.Set("q", f.Text)
	}
	if f.Agent != "" {
		v.Set("agent", f.Agent)
	}
	if f.Repo != "" {
		v.Set("repo", f.Repo)
	}
	if session != "" {
		v.Set("session", session)
	}
	if flagged {
		v.Set("flagged", "1")
	}
	if n > 0 {
		v.Set("n", strconv.Itoa(n))
	}
	if len(v) == 0 {
		return "/prompts"
	}
	return "/prompts?" + v.Encode()
}

func options(values []string, selected string) []option {
	out := make([]option, 0, len(values))
	for _, v := range values {
		out = append(out, option{Value: v, Selected: v == selected})
	}
	return out
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// timeRange formats "Oct 7 14:02–15:26", repeating the date when the
// range spans days.
func timeRange(from, to, now time.Time) string {
	s := render.Date(from, now)
	if to.Sub(from) < time.Minute {
		return s
	}
	f, t := from.In(now.Location()), to.In(now.Location())
	if f.YearDay() == t.YearDay() && f.Year() == t.Year() {
		return s + "–" + t.Format("15:04")
	}
	return s + " – " + render.Date(to, now)
}

// sinceOf is the header's "Monitored from" data (nil when nothing is recorded).
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
	PromptCount  int
	Actions      int64
	Flags        int64
	Running      bool
	AgentVersion string
}

// sessionRow is a prompt in the session's own list.
type sessionRow struct {
	N int
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
		Cwd: DisplayPath(d.Cwd, "", home), PromptCount: len(d.Prompts), Actions: d.Actions, Flags: d.Flags, Running: d.Running,
	}
	if !d.Started.IsZero() {
		h.ISO = d.Started.In(now.Location()).Format("2006-01-02T15:04")
		h.Date = render.Date(d.Started, now)
		if end := d.LastActivity; !end.IsZero() && end.After(d.Started) {
			h.End = end.In(now.Location()).Format("15:04")
			h.Duration = query.ShortDuration(end.Sub(d.Started))
		}
	}
	if len(d.Prompts) > 0 {
		pasted, line := query.PromptLine(d.Prompts[0].Text)
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
	for i, pr := range d.Prompts {
		v := newPromptView(pr, now)
		p.Prompts = append(p.Prompts, sessionRow{N: i + 1, promptView: v, Time: v.Date})
	}
	for _, sa := range d.Subagents {
		p.Subagents = append(p.Subagents, subagentView{
			Kind: sa.Kind, Task: sa.Task, Clock: sa.At.In(now.Location()).Format("15:04"), PromptNumber: sa.PromptNumber,
		})
	}
	return p
}

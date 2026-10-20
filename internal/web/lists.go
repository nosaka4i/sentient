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

// Page sizes of /prompts: the grouped view counts sessions, a single
// session or the flat list counts prompts. "Show more" adds one step.
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
	// Exactly one of three views: One (a single session, Summary + Rows),
	// Flat (all sessions in one list because a column other than newest-first
	// is sorted, FlatRows) or, by default, Groups (every session).
	One      bool
	Flat     bool
	Summary  sessionSummary
	Rows     []promptView
	FlatRows []promptView
	Groups   []sessionGroup
	Filtered bool
	// Sorting: the column heads (links), the Sort menu (phones), and, in the
	// flat view, a note with a way back to the grouped view.
	Heads       []sortHead
	SortOptions []option
	SortNote    string
	ResetSort   string
	// FlaggedHref toggles "flagged only", keeping the other filters.
	FlaggedHref string
	Clear       string // drops every filter ("" when none is set)
	MoreHref    string
	MoreText    string
}

type option struct {
	Value    string
	Label    string // shown instead of Value when set
	Selected bool
}

// sessionOption is an entry of the Session menu.
type sessionOption struct {
	Value, Label       string
	Selected, Disabled bool
}

// sortHead is a sortable column head; Href sorts by it (or reverses it).
type sortHead struct {
	Key, Label, Href, Aria string
}

// sessionSummary is the one-line description of the selected session.
type sessionSummary struct {
	ID, Short, Repo, Range, Href string
	Prompts, Actions, Flags      int64
}

// sessionGroup is one session in the grouped view: a header bar and the
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
	f.Sort, f.Asc = parseSort(q.Get("sort"))
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
		// back to the grouped view.
		if sum := l.Session; sum != nil && matchesFilters(sum.Agent, sum.Repo, f) {
			s.renderOne(w, p, l, f, now)
			return
		}
		session = ""
	}

	if !defaultSort(f) {
		flat := f
		flat.Limit = pageSize(n, promptStep, query.MaxListLimit)
		l, err := s.src.ListPrompts(r.Context(), flat)
		if err != nil {
			s.down(w, err)
			return
		}
		s.renderFlat(w, p, l, f, now)
		return
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

// ---- sorting ----

// sortLabels are the column heads, in table order.
var sortLabels = []struct{ Key, Label string }{
	{"prompt", "Prompt"}, {"date", "Date"}, {"dur", "Duration"}, {"acts", "Actions"}, {"flags", "Flags"},
}

// parseSort reads a "dur:asc" style value; anything else is date:desc
// (newest first).
func parseSort(s string) (key string, asc bool) {
	k, dir, _ := strings.Cut(s, ":")
	if k == "" || !query.ValidSort(k) {
		return "date", false
	}
	return k, dir == "asc"
}

func defaultSort(f query.PromptFilter) bool { return f.Sort == "date" && !f.Asc }

// sortParam is a sort as it appears in a URL; the default is left out.
func sortParam(key string, asc bool) string {
	if key == "date" && !asc {
		return ""
	}
	if asc {
		return key + ":asc"
	}
	return key + ":desc"
}

// firstDirAsc is the direction a column sorts in the first time it is
// clicked: text A to Z, numbers and dates highest first.
func firstDirAsc(key string) bool { return key == "prompt" }

// sortHeads builds the column heads: clicking the sorted column reverses it,
// clicking another sorts by that.
func sortHeads(f query.PromptFilter, session string) []sortHead {
	out := make([]sortHead, 0, len(sortLabels))
	for _, c := range sortLabels {
		asc, aria := firstDirAsc(c.Key), "none"
		if c.Key == f.Sort {
			asc = !f.Asc
			aria = "descending"
			if f.Asc {
				aria = "ascending"
			}
		}
		out = append(out, sortHead{
			Key: c.Key, Label: c.Label, Aria: aria,
			Href: listHref(f, session, f.Flagged, 0, sortParam(c.Key, asc)),
		})
	}
	return out
}

// sortOptions is the Sort menu (the heads aren't shown on phones).
func sortOptions(f query.PromptFilter) []option {
	menu := []struct{ Value, Label string }{
		{"date:desc", "Newest first"}, {"date:asc", "Oldest first"},
		{"dur:desc", "Longest"}, {"dur:asc", "Shortest"},
		{"acts:desc", "Most actions"}, {"acts:asc", "Fewest actions"},
		{"flags:desc", "Most flags"}, {"flags:asc", "Fewest flags"},
		{"prompt:asc", "Prompt A–Z"}, {"prompt:desc", "Prompt Z–A"},
	}
	cur := sortParam(f.Sort, f.Asc)
	if cur == "" {
		cur = "date:desc"
	}
	out := make([]option, 0, len(menu))
	for _, m := range menu {
		out = append(out, option{Value: m.Value, Label: m.Label, Selected: m.Value == cur})
	}
	return out
}

// sortNote names the sort for the flat view's note.
func sortNote(f query.PromptFilter) string {
	switch f.Sort {
	case "prompt":
		if f.Asc {
			return "prompt text, A to Z"
		}
		return "prompt text, Z to A"
	case "date":
		return "date, oldest first"
	case "dur":
		if f.Asc {
			return "duration, shortest first"
		}
		return "duration, longest first"
	case "acts":
		if f.Asc {
			return "actions, fewest first"
		}
		return "actions, most first"
	}
	if f.Asc {
		return "flags, fewest first"
	}
	return "flags, most first"
}

// ---- the three views ----

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
	p.Shown = shownText(len(l.Prompts), l.Total, p.Filtered)
	sp := sortParam(f.Sort, f.Asc)
	p.Heads, p.SortOptions = sortHeads(f, sum.ID), sortOptions(f)
	p.FlaggedHref = listHref(f, sum.ID, !f.Flagged, 0, sp)
	if p.Filtered {
		p.Clear = listHref(query.PromptFilter{}, sum.ID, false, 0, sp)
	}
	if rem := l.Total - int64(len(l.Prompts)); rem > 0 && len(l.Prompts) < query.MaxListLimit {
		p.MoreHref = listHref(f, sum.ID, f.Flagged, len(l.Prompts)+promptStep, sp)
		p.MoreText = fmt.Sprintf("%d older prompts in this session", rem)
	}
	s.render(w, http.StatusOK, "prompts", p)
}

// renderFlat shows every session's prompts in one list, sorted by a column.
func (s *Server) renderFlat(w http.ResponseWriter, p promptsPage, l *query.PromptList, f query.PromptFilter, now time.Time) {
	p.Flat = true
	p.Total = l.Total
	p.Since = sinceOf(l.HistoryBegins, now)
	p.Agents, p.Repos = options(l.Agents, f.Agent), options(l.Repos, f.Repo)
	p.Sessions = sessionOptions(l.Sessions, l.OlderSessions, nil, f)
	for _, pr := range l.Prompts {
		p.FlatRows = append(p.FlatRows, newPromptView(pr, now))
	}
	p.Shown = shownText(len(l.Prompts), l.Total, p.Filtered)
	sp := sortParam(f.Sort, f.Asc)
	p.Heads, p.SortOptions = sortHeads(f, ""), sortOptions(f)
	p.SortNote = sortNote(f)
	p.ResetSort = listHref(f, "", f.Flagged, 0, "")
	p.FlaggedHref = listHref(f, "", !f.Flagged, 0, sp)
	if p.Filtered {
		p.Clear = listHref(query.PromptFilter{}, "", false, 0, sp)
	}
	if rem := l.Total - int64(len(l.Prompts)); rem > 0 && len(l.Prompts) < query.MaxListLimit {
		p.MoreHref = listHref(f, "", f.Flagged, len(l.Prompts)+promptStep, sp)
		p.MoreText = fmt.Sprintf("%d older prompts", rem)
	}
	s.render(w, http.StatusOK, "prompts", p)
}

func shownText(shown int, total int64, filtered bool) string {
	if filtered {
		return fmt.Sprintf("%d of %d match", shown, total)
	}
	return fmt.Sprintf("%d of %d shown", shown, total)
}

func (s *Server) renderAll(w http.ResponseWriter, p promptsPage, g *query.SessionGroups, f query.PromptFilter, now time.Time) {
	p.Total = g.TotalPrompts
	p.Since = sinceOf(g.HistoryBegins, now)
	p.Agents, p.Repos = options(g.Agents, f.Agent), options(g.Repos, f.Repo)
	p.Sessions = sessionOptions(g.Sessions, g.OlderSessions, nil, f)
	p.Heads, p.SortOptions = sortHeads(f, ""), sortOptions(f)
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
			sg.SeeAll = listHref(f, sum.ID, f.Flagged, 0, "")
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
	p.FlaggedHref = listHref(f, "", !f.Flagged, 0, "")
	if p.Filtered {
		p.Clear = "/prompts"
	}
	if rem := g.TotalSessions - int64(len(g.Groups)); rem > 0 && len(g.Groups) < query.MaxSessionLimit {
		p.MoreHref = listHref(f, "", f.Flagged, len(g.Groups)+sessionStep, "")
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
// "" is the default view; n is the page size (0 = default); sort is a
// sortParam ("" = newest first).
func listHref(f query.PromptFilter, session string, flagged bool, n int, sort string) string {
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
	if sort != "" {
		v.Set("sort", sort)
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
		p.MoreHref = listHref(query.PromptFilter{}, d.ID, false, 0, "")
		p.Older = d.TotalPrompts - int64(len(d.Prompts))
	}
	for _, sa := range d.Subagents {
		p.Subagents = append(p.Subagents, subagentView{
			Kind: sa.Kind, Task: sa.Task, Clock: sa.At.In(now.Location()).Format("15:04"), PromptNumber: sa.PromptNumber,
		})
	}
	return p
}

package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/query"
	"github.com/nosaka4i/sentient/internal/render"
	"github.com/nosaka4i/sentient/internal/version"
)

// ---- /agents ----

type agentsPage struct {
	page
	Rows []agentRowView
}

type agentRowView struct {
	Href            string
	Agent, Repo     string
	Started         string
	Stopped         string
	Running         bool
	Background      string
	BackgroundCount int64
}

func (s *Server) agents(w http.ResponseWriter, r *http.Request) {
	rows, err := s.src.ListAgents(r.Context())
	if err != nil {
		s.down(w, err)
		return
	}
	now := s.now()
	p := agentsPage{page: page{Title: "agents", Version: version.Version, Nav: "agents"}}
	for _, a := range rows {
		v := agentRowView{
			Href: "/agent/" + url.PathEscape(a.ID), Agent: a.Agent, Repo: a.Repo,
			Started: render.Date(a.Started, now), Running: a.Running,
			Background: humanize.Comma(a.Background), BackgroundCount: a.Background,
		}
		if !a.Running {
			v.Stopped = render.Date(a.Stopped, now)
		}
		p.Rows = append(p.Rows, v)
	}
	s.render(w, http.StatusOK, "agents", p)
}

// ---- /agent/<id> ----

// timelinePage is how many runs one timeline page shows.
const timelinePage = 100

type agentPage struct {
	page
	ID       string
	Heading  string // claude · repo
	Running  bool
	When     string // "running since Oct 29 03:41" / "Oct 28 21:10 – 23:55"
	Sessions []agentSessionLink
	Count    string // background commands, humanized
	Timeline bool   // the timeline view; else per command
	// CommandHref and TimelineHref are the toggle's links, which keep the filter.
	CommandHref, TimelineHref string
	Groups                    []stepView
	// the timeline's filter and rows
	Q, From, To string // the filter as typed (From/To as the browser's datetime-local)
	FromLabel   string // "Oct 29 04:10 – 04:15" when a window is set
	ClearWindow string // the link that drops the window
	Rows        []timelineRow
	Matching    string // "11 of 1,204"
	PageLabel   string // "1–6 of 11"
	NextHref    string
	PrevHref    string
}

type agentSessionLink struct {
	Short, Href string
}

type timelineRow struct {
	T                string
	Pre, Match, Post string
}

// parseWhen reads a time the way the browser sends it (datetime-local, local
// time) or as RFC3339; the zero time for anything else.
func parseWhen(v string, loc *time.Location) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, v, loc); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (s *Server) agent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, err := s.src.AgentDetail(r.Context(), id)
	if errors.Is(err, api.ErrNotFound) || (err == nil && d == nil) {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	if err != nil {
		s.down(w, err)
		return
	}
	now := s.now()
	q := r.URL.Query()
	from, to := parseWhen(q.Get("from"), now.Location()), parseWhen(q.Get("to"), now.Location())
	text := strings.TrimSpace(q.Get("q"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}

	p := agentPage{
		page:     page{Title: "agent", Version: version.Version, Nav: "agents", Sub: true},
		ID:       d.ID,
		Heading:  strings.TrimSpace(d.Agent + " · " + d.Repo),
		Running:  d.Running,
		Count:    humanize.Comma(d.Background),
		Timeline: q.Get("view") == "timeline",
		Q:        text,
	}
	if d.Running {
		p.When = "running since " + render.Date(d.Started, now)
	} else {
		p.When = render.Date(d.Started, now) + " – " + clockOrDate(d.Stopped, d.Started, now)
	}
	for _, ss := range d.Sessions {
		p.Sessions = append(p.Sessions, agentSessionLink{Short: query.ShortSession(ss.ID), Href: "/session/" + url.PathEscape(ss.ID)})
	}

	base := "/agent/" + url.PathEscape(d.ID)
	filter := func(view string, off int) string {
		v := url.Values{}
		if view != "" {
			v.Set("view", view)
		}
		if text != "" {
			v.Set("q", text)
		}
		if t := q.Get("from"); t != "" {
			v.Set("from", t)
		}
		if t := q.Get("to"); t != "" {
			v.Set("to", t)
		}
		if off > 0 {
			v.Set("offset", strconv.Itoa(off))
		}
		if len(v) == 0 {
			return base
		}
		return base + "?" + v.Encode()
	}
	p.CommandHref, p.TimelineHref = base, filter("timeline", 0)

	if !p.Timeline {
		for _, g := range d.Groups {
			p.Groups = append(p.Groups, newGroupView(g, now))
		}
		s.render(w, http.StatusOK, "agent", p)
		return
	}

	p.From, p.To = q.Get("from"), q.Get("to")
	if !from.IsZero() || !to.IsZero() {
		p.FromLabel = windowLabel(from, to, now)
		v := url.Values{"view": {"timeline"}}
		if text != "" {
			v.Set("q", text)
		}
		p.ClearWindow = base + "?" + v.Encode()
	}
	t, err := s.src.AgentTimeline(r.Context(), d.ID, query.TimelineFilter{Text: text, From: from, To: to, Offset: offset, Limit: timelinePage})
	if errors.Is(err, api.ErrNotFound) {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	if err != nil {
		s.down(w, err)
		return
	}
	for _, run := range t.Runs {
		row := timelineRow{T: stamp(run.At, now)}
		row.Pre, row.Match, row.Post = splitMatch(run.Command, text)
		p.Rows = append(p.Rows, row)
	}
	p.Matching = fmt.Sprintf("%s of %s", humanize.Comma(t.Total), humanize.Comma(d.Background))
	if text == "" && from.IsZero() && to.IsZero() {
		p.Matching = humanize.Comma(t.Total)
	}
	if len(t.Runs) > 0 {
		p.PageLabel = fmt.Sprintf("%d–%d of %s", offset+1, offset+len(t.Runs), humanize.Comma(t.Total))
	}
	if int64(offset+len(t.Runs)) < t.Total {
		p.NextHref = filter("timeline", offset+len(t.Runs))
	}
	if offset > 0 {
		prev := offset - timelinePage
		if prev < 0 {
			prev = 0
		}
		p.PrevHref = filter("timeline", prev)
	}
	s.render(w, http.StatusOK, "agent", p)
}

// stamp is a date and time to the second: `Oct 29 04:10:31`, with the year only
// when it is not the current one. A time alone would read as today's the next
// day, so these pages always show the date.
func stamp(t, now time.Time) string {
	t = t.In(now.Location())
	if t.Year() != now.Year() {
		return t.Format("Jan 2 2006 15:04:05")
	}
	return t.Format("Jan 2 15:04:05")
}

// clockOrDate shows when something stopped: the time alone on the day it
// started, else the date too.
func clockOrDate(t, started, now time.Time) string {
	loc := now.Location()
	if t.In(loc).YearDay() == started.In(loc).YearDay() && t.In(loc).Year() == started.In(loc).Year() {
		return t.In(loc).Format("15:04")
	}
	return render.Date(t, now)
}

// windowLabel is the filter chip for a time window: "Oct 29 04:10 – 04:15".
func windowLabel(from, to, now time.Time) string {
	switch {
	case from.IsZero():
		return "until " + render.Date(to, now)
	case to.IsZero():
		return "from " + render.Date(from, now)
	}
	return render.Date(from, now) + " – " + clockOrDate(to, from, now)
}

// splitMatch cuts s around the first case-insensitive occurrence of sub, so
// the page can highlight it.
func splitMatch(s, sub string) (pre, match, post string) {
	if sub == "" {
		return s, "", ""
	}
	i := strings.Index(strings.ToLower(s), strings.ToLower(sub))
	if i < 0 {
		return s, "", ""
	}
	return s[:i], s[i : i+len(sub)], s[i+len(sub):]
}

// newGroupView is the row for every run of one background command.
func newGroupView(g query.BackgroundGroup, now time.Time) stepView {
	v := stepView{Group: true, Res: "command", Act: "execute", Path: g.Key}
	loc := now.Location()
	first, last := g.First.In(loc), g.Last.In(loc)
	if first.Year() == last.Year() && first.YearDay() == last.YearDay() {
		v.T = render.Date(g.First, now) + "–" + last.Format("15:04") // Oct 29 03:41–04:15
	} else {
		v.T = render.Date(g.First, now) + " – " + render.Date(g.Last, now)
	}
	v.Note = fmt.Sprintf("%s times", humanize.Comma(g.Count))
	if g.Count == 1 {
		v.Note = "once"
	}
	for _, run := range g.Latest {
		v.Runs = append(v.Runs, runView{T: stamp(run.At, now), Path: run.Command})
	}
	if more := g.Count - int64(len(g.Latest)); more > 0 {
		v.RunsMore = int(more)
	}
	return v
}

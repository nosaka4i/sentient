package web

import (
	"fmt"
	"html/template"
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

// ---- /activity ----
//
// Every resource/action count of a window as tiles, and below them the
// events of the selected tile. Server-rendered from the URL (days, resource,
// action, failed), so a tile is a plain link.

// DefaultActivityDays is the activity page's window when none is asked for
// (the dashboard tiles that link here count the last 24 hours).
const DefaultActivityDays = 1

var activityWindows = []struct {
	Days  int
	Label string
}{{1, "Last 24h"}, {7, "7 days"}, {30, "30 days"}, {0, "All time"}}

// whatHeads names the second column of the event list by resource.
var whatHeads = map[string]string{
	"file": "Path", "dir": "Path", "command": "Command", "process": "Process", "network": "Host",
	"web search": "Query", "web fetch": "URL", "git": "Command", "launch item": "Item", "prompt": "Prompt", "subagent": "Task", "monitor": "Gap",
}

// hasResult says whether events of this kind can have anything to show in
// the Result column: a failure (commands, git) or lines changed (files).
func hasResult(resource, action string) bool {
	switch resource {
	case "command", "git":
		return true
	case "file":
		return action == "create" || action == "update"
	}
	return false
}

// whatHead is the second column's heading: the exact kind's if it has one
// (web search is a query, web fetch a URL), else the resource's.
func whatHead(resource, action string) string {
	if h, ok := whatHeads[resource+" "+action]; ok {
		return h
	}
	if h, ok := whatHeads[resource]; ok {
		return h
	}
	return "What"
}

type activityState struct {
	Days             int
	Resource, Action string
	FailedOnly       bool
	Q                string // only events whose text contains this
}

func parseActivityState(v url.Values) activityState {
	st := activityState{Days: DefaultActivityDays, Resource: v.Get("resource"), Action: v.Get("action"), FailedOnly: v.Get("failed") == "1", Q: strings.TrimSpace(v.Get("q"))}
	if d, err := strconv.Atoi(v.Get("days")); err == nil {
		for _, w := range activityWindows {
			if w.Days == d {
				st.Days = d
			}
		}
	}
	if !query.KnownKind(st.Resource, st.Action) {
		st.Resource, st.Action = "", ""
	}
	return st
}

func (st activityState) href(change func(*activityState)) string {
	change(&st)
	v := url.Values{}
	if st.Days != DefaultActivityDays {
		v.Set("days", strconv.Itoa(st.Days))
	}
	if st.Resource != "" {
		v.Set("resource", st.Resource)
		v.Set("action", st.Action)
	}
	if st.FailedOnly {
		v.Set("failed", "1")
	}
	if st.Q != "" {
		v.Set("q", st.Q)
	}
	if q := v.Encode(); q != "" {
		return "/activity?" + q
	}
	return "/activity"
}

type activityPage struct {
	page
	State      activityState
	Windows    []windowChip
	Range      string
	Groups     []activityGroup
	Selected   bool
	Resource   string
	Action     string
	WhatHead   string
	ShowResult bool
	Grouped    bool           // the events sit in Sections instead of one flat list
	Sections   []eventSection // events grouped by prompt and session
	Events     []activityEventView
	Total      int64
	Shown      int
	Truncated  bool
	CanFail    bool
	FailedOnly bool
	FailedHref string
	FailedSeen int64
	KindCount  int64  // the selected kind's count in the window, before any search
	Q          string // the search in effect
	ClearHref  string // the page without it
}

type activityGroup struct {
	Name  string
	N     int
	Tiles []activityTile
}

type activityTile struct {
	Resource, Action, Href string
	Count                  int64
	Zero, Sel              bool
	SumClass               string
	Sum                    template.HTML
}

// activityWhatWidth is how many characters of a command, path or prompt an
// event row shows; the hover title has more.
const activityWhatWidth = 200

type activityEventView struct {
	ISO, Time                 string
	What, Title               string
	Failed                    bool
	Added, Removed            int64
	PromptHref, PromptText    string
	Agent, Repo, SessionShort string
	SessionHref               string
	at                        time.Time
}

func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	st := parseActivityState(r.URL.Query())
	a, err := s.src.ActivityList(r.Context(), query.ActivityRequest{Days: st.Days, Resource: st.Resource, Action: st.Action, FailedOnly: st.FailedOnly, Search: st.Q})
	if err != nil || a == nil {
		if err == nil {
			err = api.ErrUnreachable
		}
		s.down(w, err)
		return
	}
	s.render(w, http.StatusOK, "activity", newActivityPage(a, st, s.now()))
}

func newActivityPage(a *query.Activity, st activityState, now time.Time) activityPage {
	// the page shows what the daemon chose when nothing (valid) was asked for
	st.Resource, st.Action = a.Resource, a.Action
	title := "activity"
	if a.Resource != "" {
		title += " · " + a.Resource + " / " + a.Action
	}
	p := activityPage{
		page:       page{Title: title, Version: version.Version, Sub: true, Since: sinceOf(a.HistoryBegins, now)},
		State:      st,
		Selected:   a.Resource != "",
		Resource:   a.Resource,
		Action:     a.Action,
		WhatHead:   whatHead(a.Resource, a.Action),
		ShowResult: hasResult(a.Resource, a.Action),
		Total:      a.EventsTotal,
		FailedOnly: st.FailedOnly,
	}
	if a.From.IsZero() {
		p.Range = "All time"
	} else {
		p.Range = "Rolling window, " + render.Date(a.From, now) + " → now"
	}
	for _, w := range activityWindows {
		days := w.Days
		p.Windows = append(p.Windows, windowChip{Label: w.Label, On: days == st.Days, Href: st.href(func(x *activityState) { x.Days = days })})
	}

	var group *activityGroup
	for _, row := range a.Rows {
		if group == nil || group.Name != row.Resource {
			p.Groups = append(p.Groups, activityGroup{Name: row.Resource})
			group = &p.Groups[len(p.Groups)-1]
		}
		t := activityTile{
			Resource: row.Resource, Action: row.Action, Count: row.Count, Zero: row.Count == 0,
			Sel: row.Resource == a.Resource && row.Action == a.Action, SumClass: "sum", Sum: nbsp,
			Href: st.href(func(x *activityState) { x.Resource, x.Action, x.FailedOnly = row.Resource, row.Action, false }),
		}
		esc := template.HTMLEscapeString
		switch row.Resource + " " + row.Action {
		case "prompt request":
			if row.Repos > 0 {
				t.Sum = template.HTML(esc(plural(row.Repos, "repo")))
			}
		case "file read":
			if row.Outside > 0 {
				t.Sum = template.HTML(fmt.Sprintf(`<span class="warn">%d outside repo</span>`, row.Outside))
			}
		case "file update":
			if row.Added > 0 || row.Removed > 0 {
				t.SumClass = "sum mono"
				t.Sum = template.HTML(fmt.Sprintf(`<span class="add">+%d</span> <span class="del">&minus;%d</span>`, row.Added, row.Removed))
			}
		case "command execute":
			if row.Failed > 0 {
				t.Sum = template.HTML(fmt.Sprintf(`<span class="warn">%d failed</span>`, row.Failed))
			}
		case "web fetch", "network connect":
			if row.Hosts != "" {
				t.SumClass = "sum mono"
				t.Sum = template.HTML(esc(row.Hosts))
			}
		}
		if row.Resource == a.Resource && row.Action == a.Action {
			p.CanFail = row.Failed > 0 || st.FailedOnly
			p.FailedSeen = row.Failed
			p.KindCount = row.Count
		}
		group.Tiles = append(group.Tiles, t)
		group.N = len(group.Tiles)
	}
	p.FailedHref = st.href(func(x *activityState) { x.FailedOnly = !x.FailedOnly })
	p.Q = st.Q
	p.ClearHref = st.href(func(x *activityState) { x.Q = "" }) + "#events"

	for _, e := range a.Events {
		v := activityEventView{
			ISO: e.At.In(now.Location()).Format("2006-01-02T15:04"), Time: render.Date(e.At, now),
			What: e.What, Failed: e.Failed, Added: e.Added, Removed: e.Removed, Agent: e.Agent, Repo: e.Repo,
		}
		if a.Resource == "prompt" {
			_, line := query.PromptLine(e.What)
			v.What = line
		}
		// a long command or prompt would fill the page: cut it, keep the whole text in the hover title
		v.What, v.Title = query.Ellipsis(v.What, activityWhatWidth), ""
		if len([]rune(e.What)) > activityWhatWidth {
			v.Title = query.Ellipsis(e.What, 2000)
		}
		if e.PromptID != 0 {
			_, line := query.PromptLine(e.PromptText)
			v.PromptHref, v.PromptText = "/prompt/"+strconv.FormatInt(e.PromptID, 10), query.Ellipsis(line, 90)
		}
		if e.SessionID != "" {
			v.SessionHref, v.SessionShort = "/session/"+url.PathEscape(e.SessionID), query.ShortSession(e.SessionID)
		}
		v.at = e.At
		p.Events = append(p.Events, v)
	}
	p.Shown = len(p.Events)
	p.Truncated = p.Total > int64(p.Shown)
	// Events of one prompt in one session sit under one header, so neither is
	// repeated on every row. A prompt's own events are the prompts, which stay a flat list.
	if p.Grouped = a.Resource != "prompt"; p.Grouped {
		p.Sections = groupEvents(p.Events, now)
	}
	return p
}

// eventSection is a run of events that share a prompt and session.
type eventSection struct {
	PromptHref, PromptText    string
	SessionHref, SessionShort string
	Agent, Repo               string
	Span                      string // when the run happened: "Oct 21 01:41–01:44"
	Events                    []activityEventView
}

// groupEvents splits events (newest first) into runs that share a prompt and
// session, keeping their order, so each header can show them once.
func groupEvents(events []activityEventView, now time.Time) []eventSection {
	var out []eventSection
	for _, e := range events {
		if n := len(out); n == 0 || out[n-1].PromptHref != e.PromptHref || out[n-1].SessionHref != e.SessionHref {
			out = append(out, eventSection{
				PromptHref: e.PromptHref, PromptText: e.PromptText, SessionHref: e.SessionHref, SessionShort: e.SessionShort,
				Agent: e.Agent, Repo: e.Repo,
			})
		}
		s := &out[len(out)-1]
		s.Events = append(s.Events, e)
	}
	for i := range out {
		evs := out[i].Events
		newest, oldest := evs[0].at, evs[len(evs)-1].at // newest first
		out[i].Span = render.Date(oldest, now)
		if !sameDayIn(oldest, newest, now.Location()) {
			out[i].Span += " – " + render.Date(newest, now)
		} else if newest.Sub(oldest) >= time.Minute {
			out[i].Span += "–" + newest.In(now.Location()).Format("15:04")
		}
	}
	return out
}

func sameDayIn(a, b time.Time, loc *time.Location) bool {
	ay, am, ad := a.In(loc).Date()
	by, bm, bd := b.In(loc).Date()
	return ay == by && am == bm && ad == bd
}

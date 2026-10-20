package web

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nosaka4i/sentient/internal/query"
	"github.com/nosaka4i/sentient/internal/render"
	"github.com/nosaka4i/sentient/internal/version"
)

// ---- /sessions ----
//
// One row per session, filtered and sorted on the server from the URL
// (q, agent, repo, flagged, sort, days), so the page needs no script beyond
// submitting the filter form. Sessions are few next to prompts, so the whole
// window is one response.

// DefaultSessionDays is the sessions page's time window when none is asked for.
const DefaultSessionDays = 7

// sessionWindows are the windows the page offers (0 = all time).
var sessionWindows = []struct {
	Days  int
	Label string
}{{1, "24 hours"}, {7, "7 days"}, {30, "30 days"}, {0, "All time"}}

// sessionSorts are the sortable columns: key, label in the phones' Sort
// menu, and the direction a first click sorts in.
var sessionSorts = []struct {
	Key, Label, Menu, First string
}{
	{"last", "Last active", "Recently active", "desc"},
	{"sess", "Session", "Repo A–Z", "asc"},
	{"prompts", "Prompts", "Most prompts", "desc"},
	{"acts", "Actions", "Most actions", "desc"},
	{"flags", "Flags", "Most flags", "desc"},
}

// sessionsState is what the URL asks for.
type sessionsState struct {
	Q, Agent, Repo string
	Flagged        bool
	SortKey        string
	Asc            bool
	Days           int
}

func parseSessionsState(v url.Values) sessionsState {
	st := sessionsState{Q: strings.TrimSpace(v.Get("q")), Agent: v.Get("agent"), Repo: v.Get("repo"), Flagged: v.Get("flagged") == "1", SortKey: "last", Days: DefaultSessionDays}
	if key, dir, ok := strings.Cut(v.Get("sort"), ":"); ok && (dir == "asc" || dir == "desc") {
		for _, s := range sessionSorts {
			if s.Key == key {
				st.SortKey, st.Asc = key, dir == "asc"
			}
		}
	}
	if d, err := strconv.Atoi(v.Get("days")); err == nil {
		for _, w := range sessionWindows {
			if w.Days == d {
				st.Days = d
			}
		}
	}
	return st
}

// query is the URL query for this state, leaving out what is default.
func (st sessionsState) query() string {
	v := url.Values{}
	if st.Q != "" {
		v.Set("q", st.Q)
	}
	if st.Agent != "" {
		v.Set("agent", st.Agent)
	}
	if st.Repo != "" {
		v.Set("repo", st.Repo)
	}
	if st.Flagged {
		v.Set("flagged", "1")
	}
	if st.sortParam() != "last:desc" {
		v.Set("sort", st.sortParam())
	}
	if st.Days != DefaultSessionDays {
		v.Set("days", strconv.Itoa(st.Days))
	}
	return v.Encode()
}

func (st sessionsState) sortParam() string {
	if st.Asc {
		return st.SortKey + ":asc"
	}
	return st.SortKey + ":desc"
}

func (st sessionsState) href(change func(*sessionsState)) string {
	change(&st)
	if q := st.query(); q != "" {
		return "/sessions?" + q
	}
	return "/sessions"
}

type sessionsPage struct {
	page
	Total            int64
	State            sessionsState
	Agents, Repos    []string
	Heads            map[string]sortHead
	SortMenu         []sortMenuItem
	Windows          []windowChip
	WindowLabel      string
	FlagHref         string
	Rows             []sessionListRow
	Shown, Window    int
	Filtering        bool
	Older            int64
	AllTimeHref      string
	ClearFiltersHref string
	NoSessionsAtAll  bool
	NothingInWindow  bool
	NoMatchInWindow  bool
}

type sortHead struct{ Href, Aria string }
type sortMenuItem struct {
	Value, Label string
	On           bool
}
type windowChip struct {
	Label, Href string
	On          bool
}

type sessionListRow struct {
	Href, Short, ID, Agent, Repo string
	First                        string
	Pasted                       int
	ISO, Last                    string
	Prompts, Actions, Flags      string
	FlagCount                    int64
	Running                      bool
}

// thousands writes 2140 as "2,140".
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	st := parseSessionsState(r.URL.Query())
	list, err := s.src.SessionList(r.Context(), st.Days)
	if err != nil {
		s.down(w, err)
		return
	}
	s.render(w, http.StatusOK, "sessions", newSessionsPage(list, st, s.now()))
}

func newSessionsPage(l *query.SessionList, st sessionsState, now time.Time) sessionsPage {
	p := sessionsPage{
		page:   page{Title: "sessions", Nav: "sessions", Version: version.Version, Sub: true, Since: sinceOf(l.HistoryBegins, now)},
		Total:  l.Total,
		State:  st,
		Agents: l.Agents, Repos: l.Repos,
		Heads:     map[string]sortHead{},
		Window:    len(l.Sessions),
		Filtering: st.Q != "" || st.Agent != "" || st.Repo != "" || st.Flagged,
	}
	for _, w := range sessionWindows {
		days := w.Days
		p.Windows = append(p.Windows, windowChip{Label: w.Label, On: days == st.Days, Href: st.href(func(s *sessionsState) { s.Days = days })})
		if days == st.Days {
			p.WindowLabel = "the last " + strings.ToLower(w.Label)
			if days == 0 {
				p.WindowLabel = "all time"
			}
		}
	}
	for _, s := range sessionSorts {
		key, first := s.Key, s.First
		h := sortHead{Aria: "none", Href: st.href(func(x *sessionsState) { x.SortKey, x.Asc = key, first == "asc" })}
		if key == st.SortKey {
			h.Aria = "descending"
			if st.Asc {
				h.Aria = "ascending"
			}
			h.Href = st.href(func(x *sessionsState) { x.Asc = !x.Asc })
		}
		p.Heads[key] = h
		p.SortMenu = append(p.SortMenu, sortMenuItem{Value: key + ":" + first, Label: s.Menu, On: st.sortParam() == key+":"+first})
	}
	p.FlagHref = st.href(func(s *sessionsState) { s.Flagged = !s.Flagged })
	p.AllTimeHref = st.href(func(s *sessionsState) { s.Days = 0 })
	p.ClearFiltersHref = st.href(func(s *sessionsState) { s.Q, s.Agent, s.Repo, s.Flagged = "", "", "", false })
	if st.Days != 0 && l.Total > int64(len(l.Sessions)) {
		p.Older = l.Total - int64(len(l.Sessions))
	}

	var rows []query.SessionRow
	q := strings.ToLower(st.Q)
	for _, row := range l.Sessions {
		if (st.Agent != "" && row.Agent != st.Agent) || (st.Repo != "" && row.Repo != st.Repo) || (st.Flagged && row.Flags == 0) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(row.Repo+" "+row.Agent+" "+row.ID+" "+row.FirstPrompt), q) {
			continue
		}
		rows = append(rows, row)
	}
	sortSessions(rows, st)
	for _, row := range rows {
		p.Rows = append(p.Rows, newSessionListRow(row, now))
	}
	p.Shown = len(rows)
	switch {
	case len(rows) > 0:
	case l.Total == 0:
		p.NoSessionsAtAll = true
	case len(l.Sessions) == 0:
		p.NothingInWindow = true
	default:
		p.NoMatchInWindow = true
	}
	return p
}

// sortSessions orders rows by the state's column; ties go to the most
// recently active session.
func sortSessions(rows []query.SessionRow, st sessionsState) {
	less := func(a, b query.SessionRow) int {
		cmp := func(x, y int64) int {
			switch {
			case x < y:
				return -1
			case x > y:
				return 1
			}
			return 0
		}
		switch st.SortKey {
		case "sess":
			// repo name first (sessions without one last), then the id
			ra, rb := strings.ToLower(a.Repo), strings.ToLower(b.Repo)
			if (ra == "") != (rb == "") {
				if ra == "" {
					return 1
				}
				return -1
			}
			if c := strings.Compare(ra, rb); c != 0 {
				return c
			}
			return strings.Compare(a.ID, b.ID)
		case "prompts":
			return cmp(a.Prompts, b.Prompts)
		case "acts":
			return cmp(a.Actions, b.Actions)
		case "flags":
			return cmp(a.Flags, b.Flags)
		}
		return cmp(a.LastActivity.UnixNano(), b.LastActivity.UnixNano())
	}
	sort.SliceStable(rows, func(i, j int) bool {
		c := less(rows[i], rows[j])
		if st.SortKey == "sess" && !st.Asc {
			// descending flips the repo order too, but keeps repo-less sessions last
			if (rows[i].Repo == "") == (rows[j].Repo == "") {
				c = -c
			}
		} else if !st.Asc {
			c = -c
		}
		if c != 0 {
			return c < 0
		}
		return rows[i].LastActivity.After(rows[j].LastActivity)
	})
}

func newSessionListRow(row query.SessionRow, now time.Time) sessionListRow {
	v := sessionListRow{
		Href: "/session/" + url.PathEscape(row.ID), Short: query.ShortSession(row.ID), ID: row.ID, Agent: row.Agent, Repo: row.Repo,
		Prompts: thousands(row.Prompts), Actions: thousands(row.Actions), FlagCount: row.Flags, Running: row.Running,
	}
	if row.FirstPrompt != "" {
		pasted, line := query.PromptLine(row.FirstPrompt)
		v.Pasted, v.First = pasted, query.Ellipsis(line, 90)
	}
	last := row.LastActivity
	if last.IsZero() {
		last = row.Started
	}
	if !last.IsZero() {
		v.ISO = last.In(now.Location()).Format("2006-01-02T15:04")
		v.Last = render.Date(last, now)
	}
	if row.Flags > 0 {
		v.Flags = thousands(row.Flags)
	}
	return v
}

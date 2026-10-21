package web

import (
	"errors"
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

// ---- alerts: /alerts, /alert/{id}, and the dashboard's NEEDS ATTENTION ----

// attentionRows is how many open alerts the dashboard lists.
const attentionRows = 5

// alertRowView is one alert in a list.
type alertRowView struct {
	Href, SevClass, SevLabel string
	Reviewed                 bool
	Title                    string // what it is about: the path, command or a plain-words rule name
	Resource, Action         string // "" for an alert with no action (a monitor gap)
	PromptHref, PromptText   string
	ISO, Time, Where         string
	ReviewedAt               string
}

func alertTitle(a query.AlertRow, home string) string {
	switch {
	case a.Path != "":
		return DisplayPath(a.Path, "", home)
	case a.Summary != "":
		return a.Summary
	}
	return strings.ReplaceAll(a.Rule, "-", " ")
}

func newAlertRow(a query.AlertRow, now time.Time, home string) alertRowView {
	v := alertRowView{
		Href: "/alert/" + strconv.FormatInt(a.ID, 10), SevClass: a.Severity, SevLabel: strings.ToUpper(a.Severity[:1]) + a.Severity[1:],
		Reviewed: a.Status == query.AlertReviewed, Title: query.Ellipsis(alertTitle(a, home), 120),
		Resource: a.Resource, Action: a.Action,
		ISO: a.At.In(now.Location()).Format("2006-01-02T15:04"), Time: render.Date(a.At, now),
	}
	if a.PromptID != 0 {
		_, line := query.PromptLine(a.PromptText)
		v.PromptHref, v.PromptText = "/prompt/"+strconv.FormatInt(a.PromptID, 10), query.Ellipsis(line, 60)
	}
	switch {
	case a.Repo != "" && a.Agent != "":
		v.Where = a.Agent + " · " + a.Repo
	case a.Agent != "":
		v.Where = a.Agent
	default:
		v.Where = "no agent"
	}
	if !a.ReviewedAt.IsZero() {
		v.ReviewedAt = "reviewed " + render.Date(a.ReviewedAt, now)
	}
	return v
}

// ---- /alerts ----

type alertsPage struct {
	page
	Counts    query.AlertCounts
	Status    []windowChip
	Severity  []windowChip
	Days      []alertDay
	Shown     int
	Filtered  bool // a filter is on, so an empty list may only mean "nothing matches"
	ClearHref string
}

type alertDay struct {
	Label, Date string
	Rows        []alertRowView
}

// alertsState is what the URL asks for: status open (the default), reviewed
// or all; severity empty (all), high, medium or low.
type alertsState struct{ Status, Severity string }

func parseAlertsState(v url.Values) alertsState {
	st := alertsState{Status: query.AlertOpen}
	switch v.Get("status") {
	case query.AlertReviewed, "all":
		st.Status = v.Get("status")
	}
	switch v.Get("sev") {
	case "high", "medium", "low":
		st.Severity = v.Get("sev")
	}
	return st
}

func (st alertsState) href(change func(*alertsState)) string {
	change(&st)
	v := url.Values{}
	if st.Status != query.AlertOpen {
		v.Set("status", st.Status)
	}
	if st.Severity != "" {
		v.Set("sev", st.Severity)
	}
	if q := v.Encode(); q != "" {
		return "/alerts?" + q
	}
	return "/alerts"
}

func (s *Server) alerts(w http.ResponseWriter, r *http.Request) {
	st := parseAlertsState(r.URL.Query())
	f := query.AlertFilter{Severity: st.Severity}
	if st.Status != "all" {
		f.Status = st.Status
	}
	l, err := s.src.AlertList(r.Context(), f)
	if err != nil || l == nil {
		if err == nil {
			err = api.ErrUnreachable
		}
		s.down(w, err)
		return
	}
	s.render(w, http.StatusOK, "alerts", newAlertsPage(l, st, s.now(), s.home))
}

func newAlertsPage(l *query.AlertList, st alertsState, now time.Time, home string) alertsPage {
	p := alertsPage{
		page:     page{Title: "alerts", Version: version.Version, Sub: true, Since: sinceOf(l.HistoryBegins, now)},
		Counts:   l.Counts,
		Filtered: st.Status != query.AlertOpen || st.Severity != "",
	}
	for _, c := range []struct{ val, label string }{{query.AlertOpen, "Open"}, {query.AlertReviewed, "Reviewed"}, {"all", "All"}} {
		val := c.val
		p.Status = append(p.Status, windowChip{Label: c.label, On: st.Status == val, Href: st.href(func(x *alertsState) { x.Status = val })})
	}
	for _, c := range []struct{ val, label string }{{"", "All"}, {"high", "High"}, {"medium", "Medium"}, {"low", "Low"}} {
		val := c.val
		p.Severity = append(p.Severity, windowChip{Label: c.label, On: st.Severity == val, Href: st.href(func(x *alertsState) { x.Severity = val })})
	}
	p.ClearHref = "/alerts"

	var day *alertDay
	for _, a := range l.Alerts {
		label, date := dayLabel(a.At, now)
		if day == nil || day.Label != label || day.Date != date {
			p.Days = append(p.Days, alertDay{Label: label, Date: date})
			day = &p.Days[len(p.Days)-1]
		}
		day.Rows = append(day.Rows, newAlertRow(a, now, home))
	}
	p.Shown = len(l.Alerts)
	return p
}

// dayLabel names the day of t: Today, Yesterday, or the weekday, with the date.
func dayLabel(t, now time.Time) (label, date string) {
	loc := now.Location()
	t = t.In(loc)
	d := t.Format("Jan 2")
	if t.Year() != now.Year() {
		d = t.Format("Jan 2 2006")
	}
	ty, tm, td := t.Date()
	if y, m, dd := now.Date(); ty == y && tm == m && td == dd {
		return "Today", d
	}
	if y, m, dd := now.AddDate(0, 0, -1).Date(); ty == y && tm == m && td == dd {
		return "Yesterday", d
	}
	return t.Format("Monday"), d
}

// ---- /alert/{id} ----

type alertPage struct {
	page
	ID                        string
	Row                       alertRowView
	Open                      bool
	ReviewedAt                string
	Agent, Repo               string
	SessionHref, SessionShort string
	File                      *fileView
	CanRevert                 bool
	Steps                     []stepView
	EarlierSteps, LaterSteps  int
	StepLabel                 string
	Raw                       string
	Rule                      string
	NextValue                 string // the form value the review button sends
}

func (s *Server) alert(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "no such alert", http.StatusNotFound)
		return
	}
	d, err := s.src.AlertDetail(r.Context(), id)
	if errors.Is(err, api.ErrNotFound) || (err == nil && d == nil) {
		http.Error(w, "no such alert", http.StatusNotFound)
		return
	}
	if err != nil {
		s.down(w, err)
		return
	}
	var pd *query.PromptDetail
	if d.Alert.PromptID != 0 {
		// the trace and diff come from the prompt; the alert page still works without them
		if pd, err = s.src.PromptDetail(r.Context(), d.Alert.PromptID); err != nil {
			s.log.Warn("alert's prompt", "alert", id, "err", err)
			pd = nil
		}
	}
	s.render(w, http.StatusOK, "alert", newAlertPage(d, pd, s.now(), s.home))
}

func newAlertPage(d *query.AlertDetail, pd *query.PromptDetail, now time.Time, home string) alertPage {
	a := d.Alert
	row := newAlertRow(a, now, home)
	p := alertPage{
		page:       page{Title: "alert", Version: version.Version, Sub: true, Since: sinceOf(d.HistoryBegins, now)},
		ID:         strconv.FormatInt(a.ID, 10),
		Row:        row,
		Open:       a.Status != query.AlertReviewed,
		ReviewedAt: row.ReviewedAt,
		Agent:      a.Agent, Repo: a.Repo,
		Rule: a.Rule, Raw: d.RawRecord,
		NextValue: "1",
	}
	if !p.Open {
		p.NextValue = "0"
	}
	if a.SessionID != "" {
		p.SessionHref, p.SessionShort = "/session/"+url.PathEscape(a.SessionID), query.ShortSession(a.SessionID)
	}
	if pd == nil {
		return p
	}
	for _, f := range pd.Files {
		if f.Path == a.Path {
			fv := newFileView(f, true, pd.RepoRoot, home, now)
			p.File = &fv
		}
	}
	// "Revert this change" is not built yet; the page shows it as "soon"
	p.CanRevert = p.File != nil && a.Resource == "file" && a.Action == "update"
	// the trace: two steps either side of the flagged one
	at := -1
	for i, s := range pd.Steps {
		if a.ActionID != 0 && s.ActionID == a.ActionID {
			at = i
		}
	}
	if at >= 0 {
		lo, hi := max(at-2, 0), min(at+3, len(pd.Steps))
		for i := lo; i < hi; i++ {
			sv := newStepView(pd.Steps[i], true)
			sv.Flagged = i == at
			p.Steps = append(p.Steps, sv)
		}
		p.EarlierSteps, p.LaterSteps = lo, len(pd.Steps)-hi
		p.StepLabel = "step " + strconv.Itoa(at+1) + " of " + strconv.Itoa(len(pd.Steps))
	}
	return p
}

// ---- POST /alert/{id}/review ----

// review marks an alert reviewed (reviewed=1) or open again (reviewed=0) and
// goes back to the alert. It is the one write the UI makes, so it only
// accepts a request the page itself sent.
func (s *Server) review(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-site request refused", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "no such alert", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	reviewed := r.PostForm.Get("reviewed") != "0"
	err = s.src.ReviewAlert(r.Context(), id, reviewed)
	switch {
	case errors.Is(err, api.ErrNotFound):
		http.Error(w, "no such alert", http.StatusNotFound)
	case err != nil:
		s.down(w, err)
	default:
		http.Redirect(w, r, "/alert/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
	}
}

// sameOrigin reports whether a state-changing request came from this
// server's own pages: the browser's Origin (or, without one, Sec-Fetch-Site)
// must say so. A request that says neither is refused.
func sameOrigin(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		return err == nil && u.Host == r.Host
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

package web

import (
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/query"
	"github.com/nosaka4i/sentient/internal/version"
)

// DefaultWindowDays is how much history /prompts loads first; the page
// offers wider windows.
const DefaultWindowDays = 3

// ---- /prompts ----
//
// The page is a shell plus a script (static/prompts.js). The server hands
// the browser a window of prompts as compact JSON and the script renders,
// filters and sorts it, so those are instant and the server has no
// sort or filter code. /prompts embeds the first window; /prompts/data
// serves the rest (wider windows, older pages, one session).

type promptsPage struct {
	page
	Data template.JS // the first window, as JSON (json.Marshal escapes </script>)
}

// windowJSON is a PromptWindow in the compact form the script reads.
// Times are Unix milliseconds; each prompt's S indexes Sessions.
type windowJSON struct {
	Now      int64         `json:"now"`
	Days     int           `json:"days"`
	Widened  bool          `json:"widened"`
	More     bool          `json:"more"`
	Oldest   int64         `json:"oldest"`
	Matching int64         `json:"matching"`
	Agents   []string      `json:"agents"`
	Repos    []string      `json:"repos"`
	Menu     []menuJSON    `json:"menu"`
	Older    int64         `json:"older"`
	Sessions []sessionJSON `json:"sessions"`
	Prompts  []promptJSON  `json:"prompts"`
}

type menuJSON struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	Repo  string `json:"repo"`
}

type sessionJSON struct {
	ID      string `json:"id"`
	Agent   string `json:"agent"`
	Repo    string `json:"repo"`
	Started int64  `json:"started"`
	Last    int64  `json:"last"`
	Prompts int64  `json:"prompts"`
	Actions int64  `json:"actions"`
	Flags   int64  `json:"flags"`
}

type promptJSON struct {
	ID      int64  `json:"id"`
	S       int    `json:"s"`
	T       int64  `json:"t"`
	Text    string `json:"x"`           // the first line, as shown
	Body    string `json:"b,omitempty"` // the rest, only searched
	Pasted  int    `json:"p,omitempty"` // lines pasted into the prompt
	Dur     int64  `json:"d"`           // seconds
	Actions int64  `json:"a"`
	Flags   int64  `json:"f"`
	Running bool   `json:"r,omitempty"`
}

const (
	textWidth = 300  // characters of a prompt's first line the list carries
	bodyWidth = 1000 // characters of the rest, for search
)

func unixMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func newWindowJSON(w *query.PromptWindow, days int, now time.Time) windowJSON {
	out := windowJSON{
		Now: now.UnixMilli(), Days: days, Widened: w.Widened, More: w.More, Oldest: unixMs(w.Oldest),
		Matching: w.Matching, Agents: nonNil(w.Agents), Repos: nonNil(w.Repos), Older: w.OlderSessions,
		Menu: []menuJSON{}, Sessions: []sessionJSON{}, Prompts: make([]promptJSON, 0, len(w.Prompts)),
	}
	for _, m := range w.Menu {
		out.Menu = append(out.Menu, menuJSON{ID: m.ID, Agent: m.Agent, Repo: m.Repo})
	}
	index := map[string]int{}
	for i, s := range w.Sessions {
		index[s.ID] = i
		out.Sessions = append(out.Sessions, sessionJSON{
			ID: s.ID, Agent: s.Agent, Repo: s.Repo, Started: unixMs(s.Started), Last: unixMs(s.LastActivity),
			Prompts: s.Prompts, Actions: s.Actions, Flags: s.Flags,
		})
	}
	for _, p := range w.Prompts {
		pasted, line := query.PromptLine(p.Text)
		pj := promptJSON{
			ID: p.ID, S: index[p.SessionID], T: p.Started.UnixMilli(), Text: query.Ellipsis(line, textWidth), Pasted: pasted,
			Dur: int64(p.Duration / time.Second), Actions: p.Actions, Flags: p.Flags, Running: p.Running,
		}
		// Search also reaches the rest of a long prompt (the first
		// bodyWidth characters of it, whitespace collapsed).
		if all := strings.Join(strings.Fields(p.Text), " "); all != line {
			pj.Body = query.Ellipsis(all, bodyWidth)
		}
		out.Prompts = append(out.Prompts, pj)
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// windowDays reads the days parameter: absent or invalid is the default,
// 0 is all time.
func windowDays(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 3650 {
		return DefaultWindowDays
	}
	return n
}

func (s *Server) prompts(w http.ResponseWriter, r *http.Request) {
	days := windowDays(r.URL.Query().Get("days"))
	win, err := s.src.PromptWindow(r.Context(), query.WindowRequest{Days: days})
	if err != nil {
		s.down(w, err)
		return
	}
	now := s.now()
	data, err := json.Marshal(newWindowJSON(win, days, now))
	if err != nil {
		s.log.Error("encode prompt window", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p := promptsPage{
		page: page{Title: "prompts", Version: version.Version, Sub: true, Since: sinceOf(win.HistoryBegins, now)},
		Data: template.JS(data),
	}
	s.render(w, http.StatusOK, "prompts", p)
}

// promptsData serves a window as JSON: ?days=N (0 = all time), ?before=<RFC
// 3339> to page older, ?session=<id> for one session's prompts.
func (s *Server) promptsData(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := query.WindowRequest{Days: windowDays(q.Get("days")), SessionID: q.Get("session")}
	if b := q.Get("before"); b != "" {
		t, err := time.Parse(time.RFC3339Nano, b)
		if err != nil {
			http.Error(w, "before must be an RFC 3339 time", http.StatusBadRequest)
			return
		}
		req.Before = t
	}
	win, err := s.src.PromptWindow(r.Context(), req)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, api.ErrUnreachable) {
			code = http.StatusServiceUnavailable
		} else {
			s.log.Error("prompt window", "err", err)
		}
		http.Error(w, "sentient could not load prompts", code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(newWindowJSON(win, req.Days, s.now())); err != nil {
		s.log.Error("write prompt window", "err", err)
	}
}

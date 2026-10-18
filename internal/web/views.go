package web

import (
	"fmt"
	"html/template"
	"net/url"
	"strconv"
	"time"

	"github.com/nosaka4i/sentient/internal/query"
	"github.com/nosaka4i/sentient/internal/render"
)

// tile is one LAST 24H tile.
type tile struct {
	Resource, Action string
	Count            int64
	Href             string
	Wide             bool
	SumClass         string
	Sum              template.HTML
}

const nbsp = template.HTML("&nbsp;")

// tiles builds the LAST 24H tiles, styling each summary as the mock does.
func tiles(rows []query.ActivityRow) []tile {
	out := make([]tile, 0, len(rows))
	for _, r := range rows {
		t := tile{
			Resource: r.Resource, Action: r.Action, Count: r.Count,
			Href:     ActivityHref(r.Resource, r.Action),
			Wide:     r.Resource == "network" && r.Action == "connect",
			SumClass: "sum", Sum: nbsp,
		}
		esc := template.HTMLEscapeString
		switch r.Resource + " " + r.Action {
		case "prompt request":
			if r.Repos > 0 {
				t.Sum = template.HTML(esc(plural(r.Repos, "repo")))
			}
		case "file read":
			if r.Outside > 0 {
				t.Sum = template.HTML(fmt.Sprintf(`<span class="warn">%d outside repo</span>`, r.Outside))
			}
		case "file update":
			if r.Added > 0 || r.Removed > 0 {
				t.SumClass = "sum mono"
				t.Sum = template.HTML(fmt.Sprintf(`<span class="add">+%d</span> <span class="del">&minus;%d</span>`, r.Added, r.Removed))
			}
		case "command execute":
			if r.Failed > 0 {
				t.Sum = template.HTML(fmt.Sprintf(`<span class="warn">%d failed</span>`, r.Failed))
			}
		case "web search":
			if r.Hosts != "" {
				t.SumClass = "sum mono"
				t.Sum = template.HTML(esc(r.Hosts))
			}
		}
		out = append(out, t)
	}
	return out
}

func plural(n int64, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// promptView is one PROMPTS row.
type promptView struct {
	Href, SessionHref, SessionShort string
	Running                         bool
	Pasted                          int
	Text, Title                     string
	ISO, Date                       string
	Agent, Repo, Duration           string
	Actions, Flags                  int64
}

// promptWidth is how many characters of prompt text a row shows.
const promptWidth = 60

func newPromptView(p query.PromptRow, now time.Time) promptView {
	pasted, line := query.PromptLine(p.Text)
	dur := query.ShortDuration(p.Duration)
	if p.Running {
		dur += "+"
	}
	return promptView{
		Href:         "/prompt/" + strconv.FormatInt(p.ID, 10),
		SessionHref:  "/session/" + url.PathEscape(p.SessionID),
		SessionShort: query.ShortSession(p.SessionID),
		Running:      p.Running,
		Pasted:       pasted,
		Text:         query.Ellipsis(line, promptWidth),
		Title:        query.Ellipsis(line, 300),
		ISO:          p.Started.In(now.Location()).Format("2006-01-02T15:04"),
		Date:         render.Date(p.Started, now),
		Agent:        p.Agent,
		Repo:         p.Repo,
		Duration:     dur,
		Actions:      p.Actions,
		Flags:        p.Flags,
	}
}

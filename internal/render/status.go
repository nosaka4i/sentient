package render

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/nosaka4i/sentient/internal/query"
)

// SampleMarker labels sections that show placeholder data until
// correlation and rules land (design.md "Build order", step 1).
const SampleMarker = "(sample data)"

// sampleAttention is the NEEDS ATTENTION placeholder, from the mock.
var sampleAttention = [][]string{
	{"43", "Oct 7 14:52", "claude", "nosaka", "high", "file", "update", "~/.zshrc"},
	{"41", "Oct 7 14:33", "claude", "nosaka", "medium", "file", "update", "ios/Config.plist"},
	{"44", "Oct 7 15:02", "claude", "nosaka", "medium", "file", "update", "reminders.ts, scheduler.ts +1 more"},
	{"42", "Oct 7 14:40", "claude", "nosaka", "low", "file", "delete", "old_snooze_stub.ts"},
}

// promptWidth is how many characters of a prompt PROMPTS shows.
const promptWidth = 60

// promptCells formats a prompt row as the PROMPTS columns after the
// marker: DATE AGENT REPO SESSION DURATION ACTIONS FLAGS PROMPT.
func promptCells(p query.PromptRow, now time.Time) []string {
	marker, dur := "", query.ShortDuration(p.Duration)
	if p.Running {
		marker, dur = "●", dur+"+"
	}
	flags := ""
	if p.Flags > 0 {
		flags = strconv.FormatInt(p.Flags, 10)
	}
	pasted, line := query.PromptLine(p.Text)
	if pasted > 0 {
		line = strings.TrimSpace(fmt.Sprintf("[pasted %d lines] %s", pasted, line))
	}
	return []string{marker, Date(p.Started, now), p.Agent, p.Repo, query.ShortSession(p.SessionID), dur,
		strconv.FormatInt(p.Actions, 10), flags, query.Ellipsis(line, promptWidth)}
}

// Status renders `sen status` (docs/mocks/cli/status.md).
func Status(w io.Writer, st *query.Status, now time.Time, tty bool) error {
	p := &printer{w: w}
	if st.HistoryBegins.IsZero() {
		p.line("History: nothing recorded yet")
	} else {
		p.line(fmt.Sprintf("History from %s (%s)", Date(st.HistoryBegins, now), Ago(st.HistoryBegins, now)))
	}
	if st.UnknownRecords > 0 {
		p.line(fmt.Sprintf("warning: %d agent log records of unknown type were stored uninterpreted (see the sentientd log)", st.UnknownRecords))
	}

	p.line("")
	p.line(fmt.Sprintf("NEEDS ATTENTION (%d) %s", len(sampleAttention), SampleMarker))
	t := NewTable(w, tty)
	t.Row("#", "DATE", "AGENT", "REPO", "SEVERITY", "RESOURCE", "ACTION", "PATH")
	for _, r := range sampleAttention {
		t.Row(r...)
	}
	p.table(t)

	p.line("")
	p.line("LAST 24H")
	if len(st.Activity) == 0 {
		p.line("no activity yet")
	} else {
		t = NewTable(w, tty)
		t.Row("RESOURCE", "ACTION", "COUNT", "SUMMARY")
		for _, r := range st.Activity {
			t.Row(r.Resource, r.Action, strconv.FormatInt(r.Count, 10), r.Summary)
		}
		p.table(t)
	}

	p.line("")
	// The header carries the real total, not just the rows shown.
	p.line(fmt.Sprintf("PROMPTS (%d)", st.PromptsTotal))
	if len(st.Prompts) == 0 {
		p.line("no prompts yet")
	} else {
		t = NewTable(w, tty)
		t.Row("", "DATE", "AGENT", "REPO", "SESSION", "DURATION", "ACTIONS", "FLAGS", "PROMPT")
		for _, pr := range st.Prompts {
			t.Row(promptCells(pr, now)...)
		}
		p.table(t)
		if more := st.PromptsTotal - int64(len(st.Prompts)); more > 0 {
			p.line(fmt.Sprintf("… %d more (sen session <id>)", more))
		}
	}

	p.line("")
	p.line("  sen alert <#>            details for an attention item")
	p.line("  sen session <id>         prompts in a session")
	return p.err
}

// printer writes lines, keeping the first error.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) line(s string) {
	if p.err == nil {
		_, p.err = fmt.Fprintln(p.w, s)
	}
}

func (p *printer) table(t *Table) {
	if err := t.Flush(); err != nil && p.err == nil {
		p.err = err
	}
}

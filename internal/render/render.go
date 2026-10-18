// Package render draws the CLI tables. Columns are tab-separated: aligned
// with text/tabwriter in a terminal, raw tabs when piped so cut/awk work.
package render

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// Table writes rows of cells.
type Table struct {
	out io.Writer         // final destination
	w   io.Writer         // where rows go: out, or tw when aligning
	tw  *tabwriter.Writer // nil when piped
	buf bytes.Buffer      // aligned output, before trimming
	err error
}

// NewTable starts a table on w; aligned if tty.
func NewTable(w io.Writer, tty bool) *Table {
	t := &Table{out: w, w: w}
	if tty {
		t.tw = tabwriter.NewWriter(&t.buf, 0, 0, 3, ' ', 0)
		t.w = t.tw
	}
	return t
}

// Row writes one row. Every row keeps all its cells (an empty trailing
// cell too), so columns stay aligned and piped output has a fixed field
// count.
func (t *Table) Row(cells ...string) {
	if t.err != nil {
		return
	}
	_, t.err = fmt.Fprintln(t.w, strings.Join(cells, "\t"))
}

// Flush ends the table. Aligned output loses the trailing padding that
// empty last cells leave.
func (t *Table) Flush() error {
	if t.tw == nil || t.err != nil {
		return t.err
	}
	if t.err = t.tw.Flush(); t.err != nil {
		return t.err
	}
	var b strings.Builder
	for _, line := range strings.SplitAfter(t.buf.String(), "\n") {
		if line == "" {
			continue
		}
		b.WriteString(strings.TrimRight(line, " \n"))
		if strings.HasSuffix(line, "\n") {
			b.WriteByte('\n')
		}
	}
	_, t.err = io.WriteString(t.out, b.String())
	return t.err
}

// Date formats a timestamp as in the mocks: `Oct 7 14:52`, local time, the
// year added only when it isn't the current year.
func Date(t, now time.Time) string {
	t = t.In(now.Location())
	if t.Year() != now.Year() {
		return t.Format("Jan 2 2006 15:04")
	}
	return t.Format("Jan 2 15:04")
}

// Ago formats how long ago t was: `12m ago`, `6h 12m ago`, `1 day 6h ago`.
func Ago(t, now time.Time) string {
	d := now.Sub(t)
	if d < time.Minute {
		return "just now"
	}
	mins := int(d / time.Minute)
	days, hours, m := mins/(24*60), (mins/60)%24, mins%60
	switch {
	case days > 0:
		unit := "days"
		if days == 1 {
			unit = "day"
		}
		if hours == 0 {
			return fmt.Sprintf("%d %s ago", days, unit)
		}
		return fmt.Sprintf("%d %s %dh ago", days, unit, hours)
	case hours > 0:
		if m == 0 {
			return fmt.Sprintf("%dh ago", hours)
		}
		return fmt.Sprintf("%dh %dm ago", hours, m)
	default:
		return fmt.Sprintf("%dm ago", m)
	}
}

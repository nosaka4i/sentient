package render

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/query"
)

var now = time.Date(2026, 10, 7, 15, 40, 0, 0, time.Local)

func sampleStatus() *query.Status {
	return &query.Status{
		HistoryBegins: now.Add(-(30*time.Hour + 28*time.Minute)),
		Activity: []query.ActivityRow{
			row("prompt", "request", 4, "2 repos"),
			row("file", "read", 214, "38 outside repo"),
			row("file", "create", 0, ""), // zero rows still show
			row("file", "update", 9, "+248 −61"),
			row("command", "execute", 41, "2 failed"),
			row("subagent", "start", 2, ""),
		},
		PromptsTotal: 6,
		Prompts: []query.PromptRow{
			{ID: 4, Text: "add a --json flag to list", Started: at(15, 30), Duration: 12 * time.Minute, Running: true,
				Agent: "codex", Repo: "git-backlog", SessionID: "9c1e77", Actions: 18},
			{ID: 3, Text: "run the tests and commit", Started: at(15, 20), Duration: 6 * time.Minute,
				Agent: "claude", Repo: "nosaka", SessionID: "404d12", Actions: 14, Flags: 1},
			{ID: 2, Text: "make the iOS build use the staging server so that the TestFlight build works",
				Started: at(14, 45), Duration: 12 * time.Minute, Agent: "claude", Repo: "nosaka", SessionID: "404d12", Actions: 37, Flags: 1},
			{ID: 1, Text: "<pasted_content>\n" + strings.Repeat("line\n", 240) + "</pasted_content>\nthis crash log is from the reminder screen",
				Started: at(14, 2), Duration: 36 * time.Minute, Agent: "claude", Repo: "nosaka", SessionID: "404d12", Actions: 112, Flags: 2},
		},
	}
}

func at(h, m int) time.Time { return time.Date(2026, 10, 7, h, m, 0, 0, time.Local) }

func row(res, act string, n int64, summary string) query.ActivityRow {
	return query.ActivityRow{Resource: res, Action: act, Count: n, Summary: summary}
}

// The terminal layout, aligned with three spaces between columns as in
// docs/mocks/cli/status.md.
const wantTTY = `Monitored from Oct 6 09:12 (1d 6h ago)

NEEDS ATTENTION (4) (sample data)
#    DATE          AGENT    REPO     SEVERITY   RESOURCE   ACTION   PATH
43   Oct 7 14:52   claude   nosaka   high       file       update   ~/.zshrc
41   Oct 7 14:33   claude   nosaka   medium     file       update   ios/Config.plist
44   Oct 7 15:02   claude   nosaka   medium     file       update   reminders.ts, scheduler.ts +1 more
42   Oct 7 14:40   claude   nosaka   low        file       delete   old_snooze_stub.ts

LAST 24H
RESOURCE   ACTION    COUNT   SUMMARY
prompt     request   4       2 repos
file       read      214     38 outside repo
file       create    0
file       update    9       +248 −61
command    execute   41      2 failed
subagent   start     2

PROMPTS (6)
    DATE          AGENT    REPO          SESSION   DURATION   ACTIONS   FLAGS   PROMPT
●   Oct 7 15:30   codex    git-backlog   9c1e      12m+       18                add a --json flag to list
    Oct 7 15:20   claude   nosaka        404d      6m         14        1       run the tests and commit
    Oct 7 14:45   claude   nosaka        404d      12m        37        1       make the iOS build use the staging server so that the TestF…
    Oct 7 14:02   claude   nosaka        404d      36m        112       2       [pasted 240 lines] this crash log is from the reminder scre…
… 2 more (sen session <id>)

  sen alert <#>            details for an attention item
  sen session <id>         prompts in a session
`

func TestStatusTTY(t *testing.T) {
	var b bytes.Buffer
	if err := Status(&b, sampleStatus(), now, true); err != nil {
		t.Fatal(err)
	}
	if b.String() != wantTTY {
		t.Errorf("got:\n%s\nwant:\n%s", b.String(), wantTTY)
	}
}

func TestStatusPipedUsesRawTabs(t *testing.T) {
	var b bytes.Buffer
	if err := Status(&b, sampleStatus(), now, false); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"RESOURCE\tACTION\tCOUNT\tSUMMARY\n",
		"file\tread\t214\t38 outside repo\n",
		"subagent\tstart\t2\t\n",
		"file\tcreate\t0\t\n",
		"#\tDATE\tAGENT\tREPO\tSEVERITY\tRESOURCE\tACTION\tPATH\n",
		"\tDATE\tAGENT\tREPO\tSESSION\tDURATION\tACTIONS\tFLAGS\tPROMPT\n",
		"PROMPTS (6)\n",
		"●\tOct 7 15:30\tcodex\tgit-backlog\t9c1e\t12m+\t18\t\tadd a --json flag to list\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("piped output missing %q", want)
		}
	}
	if strings.Contains(out, "   ") && strings.Contains(out, "RESOURCE   ACTION") {
		t.Error("piped output was aligned with spaces")
	}
}

func TestStatusEmptyAndWarning(t *testing.T) {
	var b bytes.Buffer
	if err := Status(&b, &query.Status{UnknownRecords: 3}, now, true); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"Monitored from: nothing recorded yet\n",
		"warning: 3 agent log records of unknown type",
		"LAST 24H\nno activity yet\n",
		"PROMPTS (0)\nno prompts yet\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestStatusWriteError(t *testing.T) {
	for _, tty := range []bool{true, false} {
		if err := Status(failWriter{}, sampleStatus(), now, tty); err == nil {
			t.Errorf("tty=%v: want write error", tty)
		}
	}
}

func TestDate(t *testing.T) {
	if got := Date(time.Date(2026, 10, 7, 14, 52, 0, 0, time.Local), now); got != "Oct 7 14:52" {
		t.Errorf("Date = %q", got)
	}
	if got := Date(time.Date(2025, 12, 31, 9, 5, 0, 0, time.Local), now); got != "Dec 31 2025 09:05" {
		t.Errorf("Date other year = %q", got)
	}
}

func TestAgo(t *testing.T) {
	cases := map[time.Duration]string{
		10 * time.Second:              "just now",
		12 * time.Minute:              "12m ago",
		3 * time.Hour:                 "3h ago",
		6*time.Hour + 12*time.Minute:  "6h 12m ago",
		30 * time.Hour:                "1d 6h ago",
		48 * time.Hour:                "2d ago",
		3*24*time.Hour + 2*time.Hour:  "3d 2h ago",
		24*time.Hour + 59*time.Minute: "1d ago",
	}
	for d, want := range cases {
		if got := Ago(now.Add(-d), now); got != want {
			t.Errorf("Ago(%v) = %q, want %q", d, got, want)
		}
	}
}

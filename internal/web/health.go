package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/nosaka4i/sentient/internal/health"
	"github.com/nosaka4i/sentient/internal/query"
	"github.com/nosaka4i/sentient/internal/render"
	"github.com/nosaka4i/sentient/internal/version"
)

type healthPage struct {
	page
	OK       bool // no critical problem
	Headline string
	Warnings []health.Warning
	Checked  since

	Daemon    []htile
	DaemonOn  string // hint: sentientd · user
	API       []htile
	APIHint   string
	Storage   []htile
	DataDir   string
	Ingest    []ingestRow
	Integrity []htile
	Agents    []htile
	AgentsN   int
}

// htile is a health tile; Href makes it a link.
type htile struct {
	Label, LabelRest string // <b>Label</b> LabelRest
	Num              string
	NumSmall         string // small text after the number ("ms", "running")
	NumTxt           bool   // smaller text-style number
	NumMono          bool
	Icon             string // agent icon before the number
	Running          bool   // running dot before the number
	Check            bool   // green check after the number
	Sum              string
	SumMono          bool
	Go               bool // "→" after the summary
	Wide, Muted      bool
	Href             string
	Title            string
}

type ingestRow struct {
	Agent, Last, Lag string
	Files, Unknown   string
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	rep, err := s.src.Health(r.Context())
	if err != nil {
		s.down(w, err)
		return
	}
	s.render(w, http.StatusOK, "health", newHealthPage(rep, s.now()))
}

func newHealthPage(rep *health.Report, now time.Time) healthPage {
	p := healthPage{
		page:     page{Title: "health", Version: version.Version, Refresh: RefreshSeconds, Live: true, Nav: "health", Sub: true},
		Warnings: rep.Warnings(),
		Checked:  since{ISO: iso(rep.CheckedAt, now), Date: render.Date(rep.CheckedAt, now)},
		DataDir:  rep.Storage.DataDir,
	}
	if !rep.HistoryBegins.IsZero() {
		p.Since = &since{ISO: iso(rep.HistoryBegins, now), Date: render.Date(rep.HistoryBegins, now), Ago: render.Ago(rep.HistoryBegins, now)}
	}
	p.OK = true
	for _, w := range p.Warnings {
		if w.Critical {
			p.OK = false
		}
	}
	switch n := len(p.Warnings); {
	case !p.OK:
		p.Headline = "Integrity check failed"
	case n == 1:
		p.Headline = "Running, with 1 warning" // not "normal": the line under it lists a warning
	case n > 1:
		p.Headline = fmt.Sprintf("Running, with %d warnings", n)
	default:
		p.Headline = "All systems normal"
	}

	d := rep.Daemon
	p.DaemonOn = "sentientd · " + d.User
	cpu := htile{Label: "cpu", Num: "—", Sum: "measuring (just started)"}
	if d.CPUWindow >= 10*time.Second {
		cpu.Num, cpu.Sum = fmt.Sprintf("%.1f%%", d.CPUPercent), "avg last "+uptime(d.CPUWindow)
	}
	p.Daemon = []htile{
		{Label: "version", Num: d.Version, Sum: "sentientd"},
		{Label: "uptime", Num: uptime(now.Sub(d.Started)), Sum: "since " + render.Date(d.Started, now)},
		cpu,
		{Label: "memory", Num: humanize.Bytes(uint64(d.RSSBytes)), Sum: "resident"},
		{Label: "socket", Num: d.Socket, NumTxt: true, NumMono: true, Sum: "used by sen and this page", Wide: true},
		sudoTile(rep.Sudo),
	}

	a := rep.API
	p.APIHint = "gRPC on " + d.Socket
	lat := htile{Label: "latency", Num: "—", Sum: "no requests yet"}
	if a.LatencyCount > 0 {
		lat.Num, lat.NumSmall = msNum(a.P50), "ms"
		lat.Sum = fmt.Sprintf("p50 · p95 %s ms · p99 %s ms", msNum(a.P95), msNum(a.P99))
	}
	errs := htile{Label: "errors", LabelRest: "/ since start", Num: humanize.Comma(a.Errors), Sum: "none", SumMono: true}
	if len(a.ErrorsByCode) > 0 {
		errs.Sum = joinCounts(a.ErrorsByCode, 4)
	}
	clients := htile{Label: "clients", LabelRest: "now", Num: fmt.Sprint(len(a.Clients)), Sum: "none", SumMono: true}
	if len(a.Clients) > 0 {
		clients.Sum = strings.Join(a.Clients, " · ")
	}
	busiest := htile{Label: "busiest", LabelRest: "methods / since start", Num: "—", Sum: "no requests yet", SumMono: true, Wide: true}
	if len(a.Methods) > 0 {
		busiest.Num, busiest.Sum = a.Methods[0].Name, joinCounts(a.Methods, 5)
	}
	p.API = []htile{
		{Label: "requests", LabelRest: "/ since start", Num: humanize.Comma(a.Requests),
			Sum: fmt.Sprintf("%.1f / sec now · since %s", a.PerSecNow, render.Date(a.Since, now))},
		lat, errs, clients, busiest,
	}

	st := rep.Storage
	disk := htile{Label: "disk free", Num: "—", Sum: "unknown"}
	if st.SizeBytes > 0 {
		disk.Num = humanize.Bytes(uint64(st.FreeBytes))
		disk.Sum = "of " + humanize.Bytes(uint64(st.SizeBytes)) + " on the data dir's filesystem"
	}
	oldest := htile{Label: "oldest data", Num: "—", NumTxt: true, Sum: "nothing recorded yet"}
	if !rep.HistoryBegins.IsZero() {
		oldest.Num, oldest.Sum = render.Date(rep.HistoryBegins, now), "first record kept"
	}
	p.Storage = []htile{
		{Label: "database", Num: humanize.Bytes(uint64(st.DBBytes)), Sum: shortPath(st.DBPath), SumMono: true},
		{Label: "shadow repo", Num: "—", Sum: "not set up yet (file versions)", Muted: true},
		{Label: "raw kernel events", Num: "—", Sum: "not set up yet (kernel capture)", Muted: true, Wide: true},
		{Label: "retention", Num: "—", NumTxt: true, Sum: "not set up yet · everything is kept", Muted: true},
		oldest,
		disk,
	}

	for _, g := range rep.Ingest {
		row := ingestRow{Agent: g.Agent, Last: "—", Lag: "—", Files: humanize.Comma(g.Files), Unknown: humanize.Comma(g.Unknown)}
		if !g.LastRecord.IsZero() {
			row.Last = clock(g.LastRecord, now)
			row.Lag = lag(now.Sub(g.LastRecord))
		}
		p.Ingest = append(p.Ingest, row)
	}

	in := rep.Integrity
	chain := htile{Label: "hash chain", Num: "Not checked yet", NumTxt: true, Wide: true,
		Sum: "first full check runs at start · then every " + uptime(in.Interval)}
	switch {
	case !in.CheckedAt.IsZero() && in.OK:
		chain.Num, chain.Check = "Verified", true
		chain.Sum = fmt.Sprintf("last check %s · %s records checked · full check every %s (last %s)",
			render.Date(in.CheckedAt, now), humanize.Comma(in.Records), uptime(in.Interval), render.Date(in.FullAt, now))
	case !in.CheckedAt.IsZero():
		chain.Num = fmt.Sprintf("Broken at record #%d", in.BadRecordID)
		chain.Sum = "a stored record was changed or removed · last check " + render.Date(in.CheckedAt, now)
	}
	p.Integrity = []htile{chain,
		{Label: "monitor gaps", Num: "—", Sum: "no gap tracking yet (needs kernel capture)", Muted: true, Wide: true},
	}

	for _, ag := range rep.Agents {
		t := htile{Label: agentName(ag.Agent), Num: "—", Icon: ag.Agent, Sum: ag.Logs, SumMono: true}
		if ag.Version != "" {
			t.Num = ag.Version
		}
		p.Agents = append(p.Agents, t)
	}
	p.AgentsN = len(rep.Agents)
	live := htile{Label: "live sessions", Num: "0", Sum: "none active in the last " + uptime(query.RunningWindow)}
	if n := len(rep.Live); n > 0 {
		l := rep.Live[0]
		live = htile{Label: "live sessions", Num: fmt.Sprint(n), NumSmall: "running", Running: true,
			Sum:   strings.Join(nonEmpty(l.Agent, l.Repo, query.ShortSession(l.SessionID)), " · "),
			Go:    true,
			Href:  "/session/" + url.PathEscape(l.SessionID),
			Title: "Open the running session"}
	}
	p.Agents = append(p.Agents, live)
	return p
}

func sudoTile(s health.Sudo) htile {
	t := htile{Label: "passwordless sudo", NumTxt: true}
	switch {
	case !s.Checked:
		t.Num, t.Sum = "unknown", s.Reason
	case len(s.Passwordless) == 0:
		t.Num, t.Sum = "none", "agent accounts need a password for sudo"
	default:
		t.Num, t.Sum = strings.Join(s.Passwordless, ", "), "can sudo without a password"
	}
	return t
}

func agentName(a string) string {
	if a == "claude" {
		return "claude code"
	}
	return a
}

func nonEmpty(s ...string) []string {
	var out []string
	for _, v := range s {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func joinCounts(c []health.NamedCount, n int) string {
	var parts []string
	for i, x := range c {
		if i == n {
			break
		}
		parts = append(parts, x.Name+" "+humanize.Comma(x.Count))
	}
	return strings.Join(parts, " · ")
}

// msNum formats a latency in milliseconds: "0.4", "4", "61".
func msNum(d time.Duration) string {
	ms := float64(d) / float64(time.Millisecond)
	if ms < 10 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", ms), ".0")
	}
	return fmt.Sprintf("%.0f", ms)
}

// uptime formats a duration as "45s", "5m", "12h 32m", "3d 4h".
func uptime(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		days, h := int(d.Hours())/24, int(d.Hours())%24
		if h == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd %dh", days, h)
	}
}

// lag formats how long ago the newest record was: "0.4s", "3m", "2h".
func lag(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < 10*time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return uptime(d)
}

// clock shows a time of day for today, else the full date.
func clock(t, now time.Time) string {
	t = t.In(now.Location())
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return t.Format("15:04:05")
	}
	return render.Date(t, now)
}

func iso(t, now time.Time) string { return t.In(now.Location()).Format("2006-01-02T15:04") }

// shortPath shows the last two path elements ("data/sentient.db").
func shortPath(p string) string {
	parts := strings.Split(strings.TrimSuffix(p, "/"), "/")
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.Join(parts, "/")
}

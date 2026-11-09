package web

import (
	"fmt"
	"strconv"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/nosaka4i/sentient/internal/health"
	"github.com/nosaka4i/sentient/internal/render"
)

// levelNote is what each capture level means, for the level tile.
var levelNote = map[string]string{
	"essential": "build caches, toolchains and system libraries are not recorded",
	"extended":  "build noise is not stored (per-command counts are not built yet)",
	"complete":  "everything the policy reports is stored",
}

// captureView is the Kernel capture card: the collector (whichever platform's
// it is), what it has stored, the level and policy, and the disk.
type captureView struct {
	Tiles  []htile
	On     bool   // a collector was started; false means agent logs only
	Warn   bool   // something needs a look (a policy mismatch, a filling disk, a reconnect)
	Paused bool   // recording is paused for lack of disk
	Lost   bool   // the collector has been unreachable past the grace period
	State  string // the card's header chip
}

// captureCard builds the card. Without a collector it is one muted tile that says
// what the user has instead; that is a normal mode, so nothing in it warns.
func captureCard(rep *health.Report, now time.Time) captureView {
	c, k := rep.Capture, rep.Collector
	if !c.Enabled && !k.On {
		return captureView{State: "agent logs only", Tiles: []htile{{Label: "kernel evidence", Num: "not collected", NumTxt: true, Wide: true, Muted: true,
			Sum: "Actions are shown as the agent logged them. Nothing independent confirms them. Start sentientd with --tetragon to add kernel evidence."}}}
	}
	v := captureView{On: true, State: "recording"}

	var tiles []htile
	away := k.On && k.State == "reconnecting"
	if k.On {
		tiles = append(tiles, systemTile(k, now))
		if away {
			v.Lost = now.Sub(k.Since) >= health.CollectorGrace
			v.Warn = true
			v.State = "reconnecting"
			tiles = append(tiles, awayTile(k, now, v.Lost))
		} else if k.State == "connecting" {
			v.State = "connecting"
		}
		tiles = append(tiles, eventsTile(rep, now), lastEventTile(k, now))
	}

	if c.Enabled {
		level, policy := levelTiles(c, now)
		tiles = append(tiles, level)
		if policy.Label != "" {
			tiles = append(tiles, policy)
			v.Warn = v.Warn || policy.Warn
		}
	}

	disk := htile{Label: "disk", LabelRest: "used", Num: "—", Sum: "unknown"}
	if s := rep.Storage; s.SizeBytes > 0 {
		disk.Num, disk.NumSmall = strconv.Itoa(s.UsedPct), "%"
		disk.Sum = fmt.Sprintf("whole disk: %s free of %s", humanize.Bytes(uint64(s.FreeBytes)), humanize.Bytes(uint64(s.SizeBytes)))
		warnAt := 70
		if s.HighPct > 0 {
			disk.Sum += fmt.Sprintf(" · recording pauses at %d%%", s.HighPct)
			warnAt = s.HighPct - 10
		}
		if s.UsedPct >= warnAt {
			disk.Warn, v.Warn = true, true
		}
	}
	tiles = append(tiles, disk)
	if s := rep.Storage; s.Paused {
		v.Paused = true
		v.State = "recording paused"
		tiles = append(tiles, htile{Label: "paused", LabelRest: "since", NumTxt: true, Warn: true,
			Num: render.Date(s.PausedSince, now), Sum: uptime(now.Sub(s.PausedSince)) + " · one gap recorded · resumes at " + strconv.Itoa(s.LowPct) + "%"})
	}

	if k.On {
		tiles = append(tiles, processesTile(rep), droppedTile(k), gapsTile(rep, now))
		if k.Lost > 0 {
			v.Warn = true
		}
		if !away && k.Detail != "" {
			tiles = append(tiles, htile{Label: k.DetailLabel, Num: k.Detail, NumTxt: true, NumMono: true, Wide: true, Sum: k.System + "'s " + k.DetailLabel})
		}
	}
	v.Tiles = tiles
	return v
}

// versionLabel shows a version with a leading "v".
func versionLabel(s string) string {
	if s != "" && s[0] >= '0' && s[0] <= '9' {
		return "v" + s
	}
	return s
}

// systemTile names the capture system: Tetragon, eslogger, ...
func systemTile(k health.Collector, now time.Time) htile {
	t := htile{Label: "capture system", Num: k.System, NumSmall: versionLabel(k.Version), NumTxt: true}
	switch k.State {
	case "connected":
		t.Sum = k.Platform + " · capturing since " + render.Date(k.Since, now)
	case "reconnecting":
		t.Sum = "not connected since " + render.Date(k.Since, now)
	default:
		t.Sum = k.Platform + " · connecting"
	}
	return t
}

// awayTile says the collector cannot be reached and why. It is shaded once the
// grace period has passed, which is also when Health raises its warning.
func awayTile(k health.Collector, now time.Time, lost bool) htile {
	why := k.LastError
	if why == "" {
		why = "no answer"
	}
	return htile{Label: k.DetailLabel, Num: "not reachable for " + uptime(now.Sub(k.Since)), NumTxt: true, Wide: true, Warn: lost,
		Sum: why + " · retrying · the kernel is not recording the agent until it is back"}
}

func eventsTile(rep *health.Report, now time.Time) htile {
	k := rep.Collector
	t := htile{Label: "events", LabelRest: "/ stored", Num: humanize.Comma(k.Stored), Sum: "none yet"}
	switch {
	case rep.CatchingUp:
		t.Num, t.Sum = "—", "still counting"
	case k.Stored > 0:
		t.Sum = "since " + render.Date(k.FirstEvent, now)
	}
	return t
}

func lastEventTile(k health.Collector, now time.Time) htile {
	t := htile{Label: "last event", Num: "—", Sum: "none since sentientd started"}
	if k.LastEvent.IsZero() {
		return t
	}
	t.Num, t.NumSmall = uptime(now.Sub(k.LastEvent)), "ago"
	switch {
	case k.State == "reconnecting" && k.Latest.Set && k.Latest.Open:
		t.Sum = "gap starts " + clock(k.Latest.From, now)
	case k.PerMinute > 0:
		t.Sum = fmt.Sprintf("~%.0f / min now", k.PerMinute)
	default:
		t.Sum = "none in the last minute"
	}
	return t
}

// levelTiles are the level sentientd runs at and the level the installed
// policy was made for: one tile while they agree, two when they do not.
func levelTiles(c health.Capture, now time.Time) (level, policy htile) {
	level = htile{Label: "level", Num: c.Level, NumTxt: true, Sum: levelNote[c.Level]}
	if c.Level == "essential" {
		level.Sum = "default · " + level.Sum
	}
	policy = htile{Label: "policy", LabelRest: "/ installed", Num: "unknown", NumTxt: true,
		Sum: "no verbosity header in " + shortPath(c.PolicyFile) + ", or sentientd cannot read it · rerun sen policy and install it"}
	if !c.PolicyKnown {
		return level, policy
	}
	policy.Num = c.PolicyLevel
	installed := ""
	if !c.PolicyInstalled.IsZero() {
		installed = " · installed " + render.Date(c.PolicyInstalled, now)
	}
	if c.PolicyLevel == c.Level {
		// one tile while they agree: the policy tile would only repeat the level
		level.LabelRest = "/ policy"
		level.Check = true
		level.Sum += " · policy matches" + installed
		return level, htile{}
	}
	policy.Warn = true
	policy.Sum = "does not match sentientd · restart sentientd with --verbosity " + c.PolicyLevel +
		", or sen policy --verbosity " + c.Level + ", install it and restart Tetragon" + installed
	return level, policy
}

func processesTile(rep *health.Report) htile {
	k := rep.Collector
	if rep.CatchingUp {
		return htile{Label: "processes", LabelRest: "/ tracked", Num: "—", Sum: "still counting"}
	}
	return htile{Label: "processes", LabelRest: "/ tracked", Num: humanize.Comma(k.Processes),
		Sum: "all time · " + humanize.Comma(k.AgentProcesses) + " belong to an agent"}
}

// droppedTile counts only what sentient itself loses (its write queue
// overflowing). Tetragon's own lost-event counters are in its metrics, which
// sentient does not read, so the tile says whose count this is.
func droppedTile(k health.Collector) htile {
	return htile{Label: "dropped", Num: humanize.Comma(k.Lost), Warn: k.Lost > 0,
		Sum: fmt.Sprintf("sentient's own queue %s of %s · %s", humanize.Comma(int64(k.QueueWaiting)), humanize.Comma(int64(k.QueueSize)),
			plural(k.Reconnects, "reconnect"))}
}

func gapsTile(rep *health.Report, now time.Time) htile {
	k := rep.Collector
	t := htile{Label: "gaps", LabelRest: "/ kernel not recorded", Num: humanize.Comma(k.Gaps), Sum: "none"}
	switch {
	case rep.CatchingUp:
		t.Num, t.Sum = "—", "still counting"
	case k.Latest.Set:
		g := k.Latest
		when := render.Date(g.From, now) + "–" + render.Date(g.To, now)
		if g.Open {
			when = "since " + render.Date(g.From, now) + ", still open"
		}
		if g.Reason == health.StoppedReason && !g.Open {
			// "around": after a crash the gap starts up to a beat early (docs/heartbeat.md)
			t.Sum = "latest: " + g.Reason + " around " + render.Date(g.From, now) + " · back " + render.Date(g.To, now)
			break
		}
		t.Sum = "latest: " + trunc(g.Reason, 60) + " · " + when
	}
	return t
}

// trunc shortens s to at most n runes, with an ellipsis.
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// Package health holds the report behind the web /health page (and later
// `sen health`): sentient's own state — daemon, local API, storage,
// ingest, integrity, agents. Types only, so clients don't link the store.
package health

import (
	"strconv"
	"time"

	"github.com/dustin/go-humanize"
)

// Report is sentient's health at CheckedAt.
type Report struct {
	CheckedAt     time.Time
	HistoryBegins time.Time // oldest data kept (zero if none)

	Daemon    Daemon
	API       API
	Storage   Storage
	Ingest    []IngestAgent
	Integrity Integrity
	Agents    []Agent
	Live      []LiveSession
	Sudo      Sudo
	Capture   Capture

	// Collector is the kernel collector's live state and what it has stored;
	// Collector.On is false when sentientd runs on agent logs only.
	Collector Collector

	// CatchingUp is set while the database was too busy to answer in time
	// (sentientd starting up): HistoryBegins, Ingest and Live are then empty.
	CatchingUp bool
}

// CollectorGrace is how long a collector may be unreachable before Health
// warns, so a routine restart of the source does not flash red.
const CollectorGrace = 30 * time.Second

// Collector is the state of the kernel collector, whichever platform's it is.
type Collector struct {
	On bool // a collector was started; false means agent logs only

	System      string // "Tetragon", "eslogger"
	Platform    string // "eBPF on Linux"
	DetailLabel string // "socket", "process"
	Detail      string // where it reads from
	Version     string // the source's own version, if it said

	State      string // connecting | connected | reconnecting
	Since      time.Time
	LastError  string
	Reconnects int64
	LastEvent  time.Time // this run
	PerMinute  float64

	QueueWaiting, QueueSize int   // sentient's own write queue
	Lost                    int64 // dropped by that queue since start

	// From the database.
	Stored         int64     // kernel events stored
	FirstEvent     time.Time // the oldest of them
	Processes      int64     // processes tracked, all time
	AgentProcesses int64     // of those, attributed to an agent session
	Gaps           int64     // gaps in kernel coverage, all time
	Latest         GapNote   // the most recent gap
}

// StoppedReason is the reason of the gap sentientd writes for the time it was
// not running (docs/heartbeat.md). The Health card words that gap as "sentientd
// was stopped around ..." by matching it.
const StoppedReason = "sentientd was stopped"

// GapNote is one gap in kernel coverage.
type GapNote struct {
	Set    bool // false when there has been no gap
	From   time.Time
	To     time.Time // zero while open
	Open   bool
	Reason string
}

// Daemon is sentientd's own process.
type Daemon struct {
	Version    string
	Started    time.Time
	CPUPercent float64 // average over the last few minutes
	CPUWindow  time.Duration
	RSSBytes   int64
	Socket     string
	User       string // account sentientd runs as
	Root       bool
}

// API is the local gRPC API's traffic since sentientd started.
type API struct {
	Since        time.Time
	Requests     int64
	PerSecNow    float64 // over the last minute
	P50, P95     time.Duration
	P99          time.Duration
	LatencyCount int // samples behind the percentiles
	Errors       int64
	ErrorsByCode []NamedCount // descending
	Methods      []NamedCount // descending
	Clients      []string     // connected now, by client name
}

// NamedCount is a name with a count.
type NamedCount struct {
	Name  string
	Count int64
}

// Storage is the store on disk.
type Storage struct {
	DataDir   string
	DBPath    string
	DBBytes   int64 // database file plus WAL and shared-memory files
	FreeBytes int64 // available to sentientd on the data dir's filesystem
	SizeBytes int64 // that filesystem's size

	// HighPct is the share of that filesystem that may be used before recording
	// pauses (0 = unknown) and LowPct the share at which it resumes. Paused
	// says it is paused now, since PausedSince.
	HighPct     int
	LowPct      int
	Paused      bool
	PausedSince time.Time
	// UsedPct is the share of the filesystem in use as df prints it (used over
	// used plus available, rounded up). Reserved blocks count as neither.
	UsedPct int
}

// IngestAgent is one agent log adapter's progress.
type IngestAgent struct {
	Agent      string
	LastRecord time.Time // newest record's own timestamp (zero if none)
	Files      int64     // transcript files tracked
	Unknown    int64     // records of unknown type or unparsable lines
}

// Integrity is the hash-chain check of raw records.
type Integrity struct {
	CheckedAt   time.Time // last completed check (zero = not yet)
	FullAt      time.Time // last full (from the first record) check
	Records     int64     // records verified in the last full check plus increments since
	OK          bool
	BadRecordID int64 // first record whose hash doesn't match (when !OK)
	Interval    time.Duration
	GapTracking bool // monitor gaps need kernel capture (not yet)
}

// Capture is what sentient records of the kernel's events
// (docs/capture-verbosity.md).
type Capture struct {
	Enabled         bool   // the kernel collector is on
	Level           string // essential | extended | complete, as sentientd runs
	PolicyKnown     bool   // the installed policy was readable and says its level
	PolicyLevel     string
	PolicyFile      string
	PolicyInstalled time.Time // the policy file's modification time (zero if unknown)
}

// Agent is an agent detected from its logs.
type Agent struct {
	Agent   string
	Version string // newest version seen in its records
	Logs    string // where its logs are read from
}

// LiveSession is a session with activity in the last few minutes.
type LiveSession struct {
	SessionID    string
	Agent        string
	Repo         string
	LastActivity time.Time
}

// Sudo is the passwordless-sudo check of the agents' accounts.
type Sudo struct {
	Checked      bool
	Reason       string   // why it wasn't checked
	Passwordless []string // accounts that can sudo without a password
}

// Warnings lists the problems worth showing at the top of the page.
func (r *Report) Warnings() []Warning {
	var w []Warning
	if r.CatchingUp {
		w = append(w, Warning{
			What: "sentientd is processing logs since the last startup",
			Why: "Until it has caught up, the log counts and live sessions below may be incomplete. " +
				"This clears by itself, usually within a minute or two.",
		})
	}
	if c := r.Collector; c.On && c.State == "reconnecting" && r.CheckedAt.Sub(c.Since) >= CollectorGrace {
		why := "sentientd cannot reach " + c.System + ", so the kernel is not recording what the agent does. " +
			"Actions in this time are shown as logged by the agent and marked not compared."
		if c.LastError != "" {
			why += " Last error: " + c.LastError + "."
		}
		w = append(w, Warning{Critical: true, Headline: "Kernel capture lost",
			What: c.System + " is not reachable",
			Why:  why + " sentientd keeps retrying and records this time as a gap.",
		})
	}
	if r.Integrity.CheckedAt.After(time.Time{}) && !r.Integrity.OK {
		w = append(w, Warning{Critical: true, Headline: "Integrity check failed",
			What: "Hash chain broken",
			Why:  "A stored agent log record was changed or removed after it was written.",
		})
	}
	for _, u := range r.Sudo.Passwordless {
		w = append(w, Warning{
			What:    "Agent account '" + u + "' has passwordless sudo",
			Account: u,
			Why:     "An agent running as this user can become root and stop or tamper with sentientd and its records.",
			FixHref: "https://github.com/nosaka4i/sentient#passwordless-sudo",
		})
	}
	var unknown int64
	for _, a := range r.Ingest {
		unknown += a.Unknown
	}
	if unknown > 0 {
		w = append(w, Warning{
			What: plural(unknown, "agent log record") + " of unknown type",
			Why:  "Stored verbatim but not interpreted; the agent's log format may have changed. See the sentientd log.",
		})
	}
	if c := r.Capture; c.Enabled && c.PolicyKnown && c.PolicyLevel != c.Level {
		w = append(w, Warning{
			What: "The Tetragon policy records at " + c.PolicyLevel + ", sentientd is set to " + c.Level,
			Why: "Either restart sentientd with --verbosity " + c.PolicyLevel + " to match the policy, or rerun sen policy --verbosity " + c.Level + ", install it and restart Tetragon. Until then sentientd keeps only what " + c.Level +
				" allows, so the database stays the size you asked for, but Tetragon still does the extra work.",
		})
	}
	if s := r.Storage; s.SizeBytes > 0 {
		used := strconv.Itoa(s.UsedPct)
		ours := "sentient's database is " + humanize.Bytes(uint64(s.DBBytes)) + ". "
		warnAt := 70 // 10 points before recording pauses
		if s.HighPct > 0 {
			warnAt = s.HighPct - 10
		}
		switch {
		case s.Paused:
			w = append(w, Warning{Critical: true, Headline: "Recording paused",
				What: "Recording paused: the disk is " + used + "% used",
				Why: "sentientd stops recording at " + strconv.Itoa(s.HighPct) + "% used so the evidence is never lost to a full disk and the machine stays usable. " +
					"Kernel events are not kept while it is paused (the pause is recorded as a gap); agent logs are picked up again afterwards. " +
					"Free space, and recording resumes at " + strconv.Itoa(s.LowPct) + "% used. " + ours + "Nothing is deleted automatically.",
			})
		case s.UsedPct >= warnAt:
			why := ours + used + "% of the filesystem holding sentient's data is used."
			if s.HighPct > 0 {
				why += " At " + strconv.Itoa(s.HighPct) + "% sentientd pauses recording."
			}
			w = append(w, Warning{What: "Disk filling up", Why: why})
		}
	}
	return w
}

// Warning is one problem.
type Warning struct {
	Critical bool
	Headline string // the page's headline when this is the first critical warning
	What     string
	Account  string // set for sudo warnings
	Why      string
	FixHref  string
}

func plural(n int64, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.FormatInt(n, 10) + " " + word + "s"
}

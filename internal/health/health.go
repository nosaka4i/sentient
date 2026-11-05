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

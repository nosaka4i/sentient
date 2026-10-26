// Package daemon wires sentientd together: store, Claude Code ingester and
// the local API.
package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/attribute"
	"github.com/nosaka4i/sentient/internal/collector"
	"github.com/nosaka4i/sentient/internal/collector/tetragon"
	"github.com/nosaka4i/sentient/internal/collector/tetragon/policy"
	"github.com/nosaka4i/sentient/internal/ingest/claudecode"
	"github.com/nosaka4i/sentient/internal/store"
	"github.com/nosaka4i/sentient/internal/version"
)

// kernelQueue is how many kernel records wait for the store before the
// collector's sink starts dropping them (and recording a gap).
const kernelQueue = 50000

// attributeInterval is how often kernel processes are tied to log sessions.
const attributeInterval = 5 * time.Second

// Defaults for where sentientd keeps things (docs/design.md "Store", "Local
// API"). On Linux it is a system service: /opt/sentient/data and a socket
// group. On macOS it runs as the user for now, so everything is in the
// user's Library and there is no group.
var (
	DefaultDataDir     = defaultDataDir(runtime.GOOS, userHome())
	DefaultSocketGroup = defaultSocketGroup(runtime.GOOS)
)

func defaultDataDir(goos, home string) string {
	if goos == "darwin" && home != "" {
		return filepath.Join(home, "Library", "Application Support", "sentient", "data")
	}
	return "/opt/sentient/data"
}

func defaultSocketGroup(goos string) string {
	if goos == "darwin" {
		return ""
	}
	return "sentient"
}

func userHome() string {
	h, _ := os.UserHomeDir()
	return h
}

// Config configures the daemon.
type Config struct {
	DataDir      string
	Socket       string
	SocketGroup  string        // group allowed to use the socket; "" = owner only
	Homes        []string      // home dirs whose ~/.claude/projects is ingested
	PollInterval time.Duration // ingest poll interval
	// Tetragon is the gRPC address of a running Tetragon. Empty leaves the
	// kernel collector off, which is the log-only mode.
	Tetragon string
	// IntegrityInterval is how often the full hash-chain check runs
	// (default hourly).
	IntegrityInterval time.Duration
	Logger            *slog.Logger
	// Ready, if set, is closed once the socket is listening.
	Ready chan<- struct{}
}

// DefaultHomes returns the home directories whose ~/.claude/projects is
// ingested. Linux (a root service): /root and every directory under /home.
// macOS: the current user's home, plus every user under /Users when running
// as root.
func DefaultHomes() []string {
	return defaultHomes(runtime.GOOS, userHome(), os.Getuid() == 0, "/")
}

// defaultHomes is DefaultHomes with its inputs spelled out; root is the
// filesystem root (a temp dir in tests).
func defaultHomes(goos, home string, isRoot bool, root string) []string {
	dirs := func(pattern string, skip ...string) []string {
		var out []string
		m, _ := filepath.Glob(filepath.Join(root, pattern))
	next:
		for _, h := range m {
			for _, s := range skip {
				if filepath.Base(h) == s {
					continue next
				}
			}
			if fi, err := os.Stat(h); err == nil && fi.IsDir() {
				out = append(out, h)
			}
		}
		return out
	}
	if goos == "darwin" {
		if !isRoot {
			if home == "" {
				return nil
			}
			return []string{home}
		}
		return dirs("Users/*", "Shared", "Guest")
	}
	return append([]string{filepath.Join(root, "root")}, dirs("home/*")...)
}

// Run runs sentientd until ctx is cancelled.
func Run(ctx context.Context, cfg Config) error {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir
	}
	if cfg.Socket == "" {
		cfg.Socket = api.DefaultSocket
	}
	if len(cfg.Homes) == 0 {
		cfg.Homes = DefaultHomes()
	}
	if cfg.IntegrityInterval <= 0 {
		cfg.IntegrityInterval = time.Hour
	}

	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()

	lis, err := api.Listen(cfg.Socket, cfg.SocketGroup, log)
	if err != nil {
		return err
	}
	defer os.Remove(cfg.Socket)
	if st.MigratedFrom < store.SchemaVersion() {
		log.Info("database schema updated", "from", st.MigratedFrom, "to", store.SchemaVersion())
	}
	log.Info("sentientd started", "version", version.Version, "data_dir", cfg.DataDir,
		"socket", cfg.Socket, "homes", cfg.Homes)
	if cfg.Ready != nil {
		close(cfg.Ready)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ing := claudecode.New(st, claudecode.Config{Homes: cfg.Homes, PollInterval: cfg.PollInterval, Logger: log})

	mon := newMonitor(st, cfg, log)
	srv := api.NewServer(st.DB)
	srv.SetHealth(mon.report)

	var wg sync.WaitGroup
	errc := make(chan error, 5)
	wg.Add(3)
	go func() { defer wg.Done(); errc <- ing.Run(ctx) }()
	go func() { defer wg.Done(); errc <- api.Serve(ctx, lis, srv) }()
	go func() { defer wg.Done(); mon.run(ctx) }()
	if cfg.Tetragon != "" {
		agents := policy.AgentPrefixes(cfg.Homes)
		var coll collector.Collector = &tetragon.Collector{Addr: cfg.Tetragon, Agents: agents}
		log.Info("kernel collector on", "collector", coll.Name(), "addr", cfg.Tetragon)
		// Events are queued and written in batches, so a busy store (the
		// first import of a big transcript) slows the queue, not Tetragon.
		sink := collector.NewBufferedSink(st, coll.Name(), kernelQueue)
		att := &attribute.Attributor{St: st, Log: log, IsAgent: func(b string) bool { return policy.IsAgent(agents, b) }}
		wg.Add(3)
		go func() { defer wg.Done(); errc <- sink.Run(ctx) }()
		go func() { defer wg.Done(); errc <- coll.Run(ctx, sink) }()
		go func() { defer wg.Done(); errc <- att.Run(ctx, attributeInterval) }()
	}

	var first error
	select {
	case <-ctx.Done():
	case first = <-errc:
	}
	cancel()
	wg.Wait()
	if first != nil && !errors.Is(first, context.Canceled) {
		return first
	}
	log.Info("sentientd stopped")
	return nil
}

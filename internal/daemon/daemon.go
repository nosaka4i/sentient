// Package daemon wires sentientd together: store, Claude Code ingester and
// the local API.
package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/ingest/claudecode"
	"github.com/nosaka4i/sentient/internal/store"
	"github.com/nosaka4i/sentient/internal/version"
)

// Production defaults (docs/design.md "Store", "Local API").
const (
	DefaultDataDir     = "/opt/sentient/data"
	DefaultSocketGroup = "sentient"
)

// Config configures the daemon.
type Config struct {
	DataDir      string
	Socket       string
	SocketGroup  string        // group allowed to use the socket; "" = owner only
	Homes        []string      // home dirs whose ~/.claude/projects is ingested
	PollInterval time.Duration // ingest poll interval
	// IntegrityInterval is how often the full hash-chain check runs
	// (default hourly).
	IntegrityInterval time.Duration
	Logger            *slog.Logger
	// Ready, if set, is closed once the socket is listening.
	Ready chan<- struct{}
}

// DefaultHomes returns /root and every directory under /home.
func DefaultHomes() []string {
	homes := []string{"/root"}
	m, _ := filepath.Glob("/home/*")
	for _, h := range m {
		if fi, err := os.Stat(h); err == nil && fi.IsDir() {
			homes = append(homes, h)
		}
	}
	return homes
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
	errc := make(chan error, 2)
	wg.Add(3)
	go func() { defer wg.Done(); errc <- ing.Run(ctx) }()
	go func() { defer wg.Done(); errc <- api.Serve(ctx, lis, srv) }()
	go func() { defer wg.Done(); mon.run(ctx) }()

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

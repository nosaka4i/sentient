package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/daemon"
	"github.com/nosaka4i/sentient/internal/version"
)

func newRootCmd() *cobra.Command {
	cfg := daemon.Config{}
	cmd := &cobra.Command{
		Use:           "sentientd",
		Short:         "sentient monitor daemon",
		Long:          "sentientd — sentient's monitor: ingests agent logs, owns the store, serves the local API.",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			cfg.Logger = slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
			return daemon.Run(ctx, cfg)
		},
	}
	cmd.CompletionOptions.HiddenDefaultCmd = true
	f := cmd.Flags()
	f.StringVar(&cfg.DataDir, "data-dir", daemon.DefaultDataDir, "directory holding sentient.db")
	f.StringVar(&cfg.Socket, "socket", api.DefaultSocket, "Unix socket for the local API")
	f.StringVar(&cfg.SocketGroup, "socket-group", daemon.DefaultSocketGroup, "group allowed to connect to the socket (empty: owner only)")
	f.StringSliceVar(&cfg.Homes, "home", nil, "home directory whose ~/.claude/projects to ingest; repeatable (default /root and /home/*)")
	f.DurationVar(&cfg.PollInterval, "poll-interval", 2*time.Second, "how often transcripts are checked for new records")
	cmd.SetContext(context.Background())
	return cmd
}

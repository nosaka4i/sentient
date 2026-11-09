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
	"github.com/nosaka4i/sentient/internal/collector/tetragon"
	"github.com/nosaka4i/sentient/internal/collector/tetragon/policy"
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
			// A log pipe that closes first (Ctrl-C also ends the `tee` it is piped
			// through) must not kill the daemon on its next log line: a write then
			// fails quietly, and the clean stop is still recorded.
			signal.Ignore(syscall.SIGPIPE)
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
	f.StringVar(&cfg.Tetragon, "tetragon", "", "read kernel events from Tetragon's gRPC address (bare --tetragon: "+tetragon.DefaultAddr+"); off by default")
	f.Lookup("tetragon").NoOptDefVal = tetragon.DefaultAddr
	f.StringVar((*string)(&cfg.Verbosity), "verbosity", "essential", "how much the kernel reports is stored: essential (leaves out build caches, toolchains and system libraries), extended or complete; match it to the policy from sen policy --verbosity")
	f.StringVar(&cfg.PolicyFile, "policy-file", policy.DefaultPolicyFile, "the installed Tetragon policy whose level is compared with --verbosity")
	f.IntVar(&cfg.DiskHigh, "disk-high-threshold", daemon.DefaultDiskHigh, "pause recording when this percent of the data disk is used (10-99); Health warns 10 points before")
	f.IntVar(&cfg.DiskLow, "disk-low-threshold", 0, "resume recording when the disk is down to this percent used (default: 5 below the high threshold)")
	f.DurationVar(&cfg.IntegrityInterval, "integrity-interval", time.Hour, "how often the full hash-chain check of the agent log records and kernel events runs")
	f.IntVar(&cfg.IntegritySlice, "integrity-slice", 2000, "records the hash-chain check reads at a time before it lets go of the database; smaller gets in the way less")
	f.DurationVar(&cfg.IntegrityPause, "integrity-pause", 200*time.Millisecond, "how long the hash-chain check waits between slices; longer uses less of the machine and takes longer (0 = no pause)")
	f.DurationVar(&cfg.IntegrityStartDelay, "integrity-start-delay", 30*time.Second, "how long after the start the first full hash-chain check waits, so the start-up import is not slowed (0 = start at once)")
	cmd.SetContext(context.Background())
	return cmd
}

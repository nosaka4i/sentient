package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/render"
	"github.com/nosaka4i/sentient/internal/version"
)

// socketEnv overrides the default socket path (e.g. for a dev sentientd).
const socketEnv = "SENTIENT_SOCKET"

// nowFunc is the clock; tests replace it.
var nowFunc = time.Now

func newRootCmd() *cobra.Command {
	cobra.EnableCommandSorting = false // status, version, help — as in docs/mocks/cli/sen.md
	root := &cobra.Command{
		Use:           "sen",
		Short:         "see what your coding agent actually did",
		Long:          "sen — see what your coding agent actually did",
		SilenceUsage:  true,
		SilenceErrors: true,
		// No Run: a command without one prints help (and no "sen [flags]" usage line).
	}
	root.CompletionOptions.HiddenDefaultCmd = true
	root.AddCommand(newStatusCmd(), newServeCmd(), newVersionCmd())
	return root
}

// resolveSocket picks the sentientd socket: flag, then $SENTIENT_SOCKET,
// then the production default.
func resolveSocket(flag string) string {
	if flag != "" {
		return flag
	}
	if env := os.Getenv(socketEnv); env != "" {
		return env
	}
	return api.DefaultSocket
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the sen version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "sen", version.Version)
			return err
		},
	}
}

func newStatusCmd() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"st"},
		Short:   "What needs your attention, and the last 24h of activity",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd, resolveSocket(socket))
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "sentientd socket (default $"+socketEnv+" or "+api.DefaultSocket+")")
	return cmd
}

func runStatus(cmd *cobra.Command, socket string) error {
	c, err := api.Dial(socket)
	if err != nil {
		return err
	}
	defer c.Close()
	now := nowFunc()
	// LAST 24H: a rolling window, not calendar today (docs/mocks/cli/status.md).
	st, _, err := c.Status(cmd.Context(), now.Add(-24*time.Hour), now)
	if errors.Is(err, api.ErrUnreachable) {
		return fmt.Errorf("%w — is it running? (systemctl status sentientd)", err)
	}
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	return render.Status(out, st, now, isTTY(out))
}

func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && (isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd()))
}

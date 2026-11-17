// es-replay plays a prototype Endpoint Security capture into sentientd, as if
// the production SentientES extension were sending it. It is a dev tool for
// the time before that extension exists: the events are real, the process
// identity (pid version, start time, uid) is approximated, so it exercises
// sentientd's collector, store and pages, not the Swift side.
//
//	sentientd --endpoint-security=/tmp/es.sock ...        (one terminal; note the =)
//	go run ./tools/dev/es-replay --socket /tmp/es.sock es-20261116-091422.jsonl
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/nosaka4i/sentient/internal/collector/es"
)

func newRootCmd() *cobra.Command {
	var socket string
	var speed float64
	var uid int64
	var once bool
	cmd := &cobra.Command{
		Use:           "es-replay <capture.jsonl>",
		Short:         "Play a prototype Endpoint Security capture into sentientd",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			conn, err := net.Dial("unix", socket)
			if err != nil {
				return fmt.Errorf("sentientd is not listening on %s (start it with --endpoint-security=%s): %w", socket, socket, err)
			}
			defer conn.Close()
			go io.Copy(io.Discard, conn) // the daemon's config reply: not used by a replay

			conv := &Converter{UID: uid}
			st, err := Replay(ctx, conv, f, conn, speed, time.Sleep)
			fmt.Fprintf(cmd.ErrOrStderr(), "replayed %d lines: %d messages sent, %d left out (closes, terminal writes), %d unreadable\n",
				st.Lines, st.Sent, st.Skipped, st.Unusable)
			if err != nil || once {
				return err
			}
			// stay connected, like the extension, so Health shows the collector as connected
			fmt.Fprintln(cmd.ErrOrStderr(), "staying connected; Ctrl-C to stop")
			return hold(ctx, conv, conn, 5*time.Second)
		},
	}
	f := cmd.Flags()
	f.StringVar(&socket, "socket", es.DefaultSocket, "the socket sentientd listens on (--endpoint-security)")
	f.Float64Var(&speed, "speed", 0, "0 sends as fast as possible; 1 follows the capture's timing; 10 is ten times faster")
	f.Int64Var(&uid, "uid", 501, "the user id given to every process (the prototype did not record it)")
	f.BoolVar(&once, "once", false, "disconnect when the capture ends, instead of staying connected")
	return cmd
}

// hold keeps the connection open with a ping every interval until ctx ends.
func hold(ctx context.Context, conv *Converter, w io.Writer, every time.Duration) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-t.C:
			if err := writePing(w, conv, now); err != nil {
				return err
			}
		}
	}
}

func main() {
	if err := newRootCmd().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "es-replay:", err)
		os.Exit(1)
	}
}

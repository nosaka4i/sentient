package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/web"
)

// DefaultAddr is where sen serve listens.
const DefaultAddr = "127.0.0.1:7357"

func newServeCmd() *cobra.Command {
	var socket, addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the web dashboard on localhost",
		Long: "Serve the web dashboard on localhost (default http://" + DefaultAddr + ").\n" +
			"It only listens on loopback; reach it remotely through an SSH tunnel.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return runServe(ctx, cmd, resolveSocket(socket), addr, nil)
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "sentientd socket (default $"+socketEnv+" or "+api.DefaultSocket+")")
	cmd.Flags().StringVar(&addr, "addr", DefaultAddr, "loopback address to listen on")
	return cmd
}

// checkLoopback refuses non-loopback listen addresses: the dashboard
// shows everything the agent did and has no login.
func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--addr %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("--addr %q is not a loopback address; sen serve only listens on localhost (use an SSH tunnel for remote access)", addr)
}

// runServe serves until ctx is done. ready, if set, receives the bound
// address once listening (for tests).
func runServe(ctx context.Context, cmd *cobra.Command, socket, addr string, ready chan<- string) error {
	if err := checkLoopback(addr); err != nil {
		return err
	}
	c, err := api.Dial(socket, "sen serve")
	if err != nil {
		return err
	}
	defer c.Close()
	log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
	srv, err := web.New(c, socket, log)
	if err != nil {
		return err
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	fmt.Fprintf(cmd.OutOrStdout(), "sen serve: http://%s (sentientd socket %s)\n", lis.Addr(), socket)
	if ready != nil {
		ready <- lis.Addr().String()
	}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(lis) }()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return hs.Shutdown(sctx)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// Package api is the local gRPC API between sentientd and sen, served
// over a Unix domain socket (docs/design.md "Local API").
package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/nosaka4i/sentient/internal/api/sentientpb"
	"github.com/nosaka4i/sentient/internal/query"
	"github.com/nosaka4i/sentient/internal/version"
)

// DefaultSocket is where sentientd listens in production.
const DefaultSocket = "/run/sentient/sentientd.sock"

// Server implements the Sentient service on top of the store.
type Server struct {
	sentientpb.UnimplementedSentientServer
	db *sql.DB
}

// NewServer returns a server reading db.
func NewServer(db *sql.DB) *Server { return &Server{db: db} }

// Status implements sentientpb.SentientServer.
func (s *Server) Status(ctx context.Context, req *sentientpb.StatusRequest) (*sentientpb.StatusResponse, error) {
	if req.GetFrom() == nil || req.GetTo() == nil {
		return nil, status.Error(codes.InvalidArgument, "from and to are required")
	}
	limit := int(req.GetPromptLimit())
	if limit < 0 || limit > 100 {
		return nil, status.Error(codes.InvalidArgument, "prompt_limit must be 0..100")
	}
	st, err := query.GetStatus(ctx, s.db, req.GetFrom().AsTime(), req.GetTo().AsTime(), limit)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "status: %v", err)
	}
	resp := &sentientpb.StatusResponse{
		UnknownRecords: st.UnknownRecords,
		DaemonVersion:  version.Version,
		PromptsTotal:   st.PromptsTotal,
	}
	if !st.HistoryBegins.IsZero() {
		resp.HistoryBegins = timestamppb.New(st.HistoryBegins)
	}
	for _, r := range st.Activity {
		resp.Activity = append(resp.Activity, &sentientpb.ActivityRow{
			Resource: r.Resource, Action: r.Action, Count: r.Count, Summary: r.Summary,
			Repos: r.Repos, Outside: r.Outside, Added: r.Added, Removed: r.Removed, Failed: r.Failed, Hosts: r.Hosts,
		})
	}
	for _, p := range st.Prompts {
		resp.Prompts = append(resp.Prompts, &sentientpb.Prompt{
			Id: p.ID, Text: p.Text, Started: timestamppb.New(p.Started), Duration: durationpb.New(p.Duration),
			Running: p.Running, Agent: p.Agent, Repo: p.Repo, SessionId: p.SessionID, Actions: p.Actions, Flags: p.Flags,
		})
	}
	return resp, nil
}

// Listen creates the Unix socket at path. A stale socket file left by a
// crashed sentientd is removed; a live one is an error. The socket is made
// group-accessible (0660) and, if group names an existing group, chgrp'd
// to it, so members of that group can run sen.
func Listen(path, group string, log *slog.Logger) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, fmt.Errorf("another sentientd is already listening on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	}
	lis, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		lis.Close()
		return nil, err
	}
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			log.Warn("socket group not found; only the owner can connect", "group", group, "socket", path)
		} else if gid, err := strconv.Atoi(g.Gid); err == nil {
			if err := os.Chown(path, -1, gid); err != nil {
				log.Warn("could not set socket group", "group", group, "err", err)
			}
		}
	}
	return lis, nil
}

// Serve serves the API on lis until ctx is cancelled.
func Serve(ctx context.Context, lis net.Listener, srv *Server) error {
	g := grpc.NewServer()
	sentientpb.RegisterSentientServer(g, srv)
	errc := make(chan error, 1)
	go func() { errc <- g.Serve(lis) }()
	select {
	case <-ctx.Done():
		g.GracefulStop()
		return nil
	case err := <-errc:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	}
}

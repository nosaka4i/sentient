package api

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/nosaka4i/sentient/internal/api/sentientpb"
	"github.com/nosaka4i/sentient/internal/health"
	"github.com/nosaka4i/sentient/internal/query"
)

// ErrUnreachable means sentientd could not be reached on its socket.
var ErrUnreachable = errors.New("sentientd is not reachable")

// Client talks to sentientd.
type Client struct {
	conn   *grpc.ClientConn
	c      sentientpb.SentientClient
	socket string
}

// Dial prepares a client for the socket at path; the connection is made
// on the first call. name identifies the client to sentientd ("sen
// status", "sen serve") for the health page.
func Dial(path, name string) (*Client, error) {
	// unix://<abs path>: a relative path would be parsed as an authority.
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient("unix://"+abs,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(clientInterceptor(name)))
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, c: sentientpb.NewSentientClient(conn), socket: path}, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// callErr maps transport failures to ErrUnreachable.
func (c *Client) callErr(err error) error {
	if s, ok := status.FromError(err); ok && (s.Code() == codes.Unavailable || s.Code() == codes.DeadlineExceeded) {
		return fmt.Errorf("%w on %s", ErrUnreachable, c.socket)
	}
	return err
}

// Health fetches sentient's health report.
func (c *Client) Health(ctx context.Context) (*health.Report, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.c.Health(ctx, &sentientpb.HealthRequest{})
	if err != nil {
		return nil, c.callErr(err)
	}
	return healthFromProto(resp), nil
}

// Status fetches status for the window [from, to). It also returns
// sentientd's version.
func (c *Client) Status(ctx context.Context, from, to time.Time) (*query.Status, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := c.c.Status(ctx, &sentientpb.StatusRequest{From: timestamppb.New(from), To: timestamppb.New(to)})
	if err != nil {
		return nil, "", c.callErr(err)
	}
	st := &query.Status{UnknownRecords: resp.GetUnknownRecords(), PromptsTotal: resp.GetPromptsTotal()}
	if resp.GetHistoryBegins() != nil {
		st.HistoryBegins = resp.GetHistoryBegins().AsTime()
	}
	for _, r := range resp.GetActivity() {
		st.Activity = append(st.Activity, query.ActivityRow{
			Resource: r.GetResource(), Action: r.GetAction(), Count: r.GetCount(), Summary: r.GetSummary(),
			Repos: r.GetRepos(), Outside: r.GetOutside(), Added: r.GetAdded(), Removed: r.GetRemoved(),
			Failed: r.GetFailed(), Hosts: r.GetHosts(),
		})
	}
	for _, p := range resp.GetPrompts() {
		st.Prompts = append(st.Prompts, query.PromptRow{
			ID: p.GetId(), Text: p.GetText(), Started: p.GetStarted().AsTime(), Duration: p.GetDuration().AsDuration(),
			Running: p.GetRunning(), Agent: p.GetAgent(), Repo: p.GetRepo(), SessionID: p.GetSessionId(),
			Actions: p.GetActions(), Flags: p.GetFlags(),
		})
	}
	return st, resp.GetDaemonVersion(), nil
}

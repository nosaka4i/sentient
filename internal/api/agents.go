package api

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/nosaka4i/sentient/internal/api/sentientpb"
	"github.com/nosaka4i/sentient/internal/query"
)

// ListAgents implements sentientpb.SentientServer.
func (s *Server) ListAgents(ctx context.Context, _ *sentientpb.ListAgentsRequest) (*sentientpb.ListAgentsResponse, error) {
	rows, err := query.ListAgents(ctx, s.db)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "agents: %v", err)
	}
	r := &sentientpb.ListAgentsResponse{}
	for _, a := range rows {
		r.Agents = append(r.Agents, agentRowToProto(a))
	}
	return r, nil
}

// AgentDetail implements sentientpb.SentientServer.
func (s *Server) AgentDetail(ctx context.Context, req *sentientpb.AgentDetailRequest) (*sentientpb.AgentDetailResponse, error) {
	d, err := query.GetAgent(ctx, s.db, req.GetId())
	if errors.Is(err, query.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "no such agent")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "agent: %v", err)
	}
	r := &sentientpb.AgentDetailResponse{Agent: agentRowToProto(d.AgentRow), Dir: d.Dir}
	for _, ss := range d.Sessions {
		r.Sessions = append(r.Sessions, ss.ID)
	}
	for _, g := range d.Groups {
		pg := &sentientpb.BackgroundGroup{Key: g.Key, Count: g.Count, First: timestamppb.New(g.First), Last: timestamppb.New(g.Last)}
		for _, run := range g.Latest {
			pg.Latest = append(pg.Latest, runToProto(run))
		}
		r.Groups = append(r.Groups, pg)
	}
	return r, nil
}

// AgentTimeline implements sentientpb.SentientServer.
func (s *Server) AgentTimeline(ctx context.Context, req *sentientpb.AgentTimelineRequest) (*sentientpb.AgentTimelineResponse, error) {
	f := query.TimelineFilter{Text: req.GetText(), Offset: int(req.GetOffset()), Limit: int(req.GetLimit())}
	if req.GetFrom() != nil {
		f.From = req.GetFrom().AsTime()
	}
	if req.GetTo() != nil {
		f.To = req.GetTo().AsTime()
	}
	t, err := query.AgentTimeline(ctx, s.db, req.GetId(), f)
	if errors.Is(err, query.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "no such agent")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "agent timeline: %v", err)
	}
	r := &sentientpb.AgentTimelineResponse{Total: t.Total}
	for _, run := range t.Runs {
		r.Runs = append(r.Runs, runToProto(run))
	}
	return r, nil
}

// ListAgents fetches the agent processes.
func (c *Client) ListAgents(ctx context.Context) ([]query.AgentRow, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := c.c.ListAgents(ctx, &sentientpb.ListAgentsRequest{})
	if err != nil {
		return nil, c.callErr(err)
	}
	out := make([]query.AgentRow, 0, len(resp.GetAgents()))
	for _, a := range resp.GetAgents() {
		out = append(out, agentRowFromProto(a))
	}
	return out, nil
}

// AgentDetail fetches one agent process, or ErrNotFound.
func (c *Client) AgentDetail(ctx context.Context, id string) (*query.AgentDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := c.c.AgentDetail(ctx, &sentientpb.AgentDetailRequest{Id: id})
	if status.Code(err) == codes.NotFound {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, c.callErr(err)
	}
	d := &query.AgentDetail{AgentRow: agentRowFromProto(resp.GetAgent()), Dir: resp.GetDir()}
	for _, id := range resp.GetSessions() {
		d.Sessions = append(d.Sessions, query.AgentSession{ID: id})
	}
	for _, g := range resp.GetGroups() {
		qg := query.BackgroundGroup{Key: g.GetKey(), Count: g.GetCount(), First: g.GetFirst().AsTime(), Last: g.GetLast().AsTime()}
		for _, run := range g.GetLatest() {
			qg.Latest = append(qg.Latest, runFromProto(run))
		}
		d.Groups = append(d.Groups, qg)
	}
	return d, nil
}

// AgentTimeline fetches one page of an agent's background commands, or ErrNotFound.
func (c *Client) AgentTimeline(ctx context.Context, id string, f query.TimelineFilter) (*query.Timeline, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req := &sentientpb.AgentTimelineRequest{Id: id, Text: f.Text, Offset: int32(f.Offset), Limit: int32(f.Limit)}
	if !f.From.IsZero() {
		req.From = timestamppb.New(f.From)
	}
	if !f.To.IsZero() {
		req.To = timestamppb.New(f.To)
	}
	resp, err := c.c.AgentTimeline(ctx, req)
	if status.Code(err) == codes.NotFound {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, c.callErr(err)
	}
	t := &query.Timeline{Total: resp.GetTotal()}
	for _, run := range resp.GetRuns() {
		t.Runs = append(t.Runs, runFromProto(run))
	}
	return t, nil
}

func agentRowToProto(a query.AgentRow) *sentientpb.AgentRow {
	r := &sentientpb.AgentRow{
		Id: a.ID, Agent: a.Agent, Repo: a.Repo, Version: a.Version, Started: timestamppb.New(a.Started),
		Running: a.Running, Background: a.Background,
	}
	if !a.Stopped.IsZero() {
		r.Stopped = timestamppb.New(a.Stopped)
	}
	return r
}

func agentRowFromProto(r *sentientpb.AgentRow) query.AgentRow {
	a := query.AgentRow{
		ID: r.GetId(), Agent: r.GetAgent(), Repo: r.GetRepo(), Version: r.GetVersion(), Started: r.GetStarted().AsTime(),
		Running: r.GetRunning(), Background: r.GetBackground(),
	}
	if r.GetStopped() != nil {
		a.Stopped = r.GetStopped().AsTime()
	}
	return a
}

func runToProto(r query.BackgroundRun) *sentientpb.BackgroundRun {
	return &sentientpb.BackgroundRun{At: timestamppb.New(r.At), Command: r.Command, Key: r.Key}
}

func runFromProto(r *sentientpb.BackgroundRun) query.BackgroundRun {
	return query.BackgroundRun{At: r.GetAt().AsTime(), Command: r.GetCommand(), Key: r.GetKey()}
}

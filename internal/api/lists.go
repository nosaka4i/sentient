package api

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/nosaka4i/sentient/internal/api/sentientpb"
	"github.com/nosaka4i/sentient/internal/query"
)

// ListPrompts implements sentientpb.SentientServer.
func (s *Server) ListPrompts(ctx context.Context, req *sentientpb.ListPromptsRequest) (*sentientpb.ListPromptsResponse, error) {
	if req.GetLimit() < 0 || req.GetLimit() > query.MaxListLimit {
		return nil, status.Errorf(codes.InvalidArgument, "limit must be 0..%d", query.MaxListLimit)
	}
	if !query.ValidSort(req.GetSort()) {
		return nil, status.Errorf(codes.InvalidArgument, "sort must be one of %v", query.SortKeys)
	}
	l, err := query.ListPrompts(ctx, s.db, query.PromptFilter{
		Text: req.GetText(), Agent: req.GetAgent(), Repo: req.GetRepo(), Flagged: req.GetFlagged(),
		SessionID: req.GetSessionId(), Limit: int(req.GetLimit()), Sort: req.GetSort(), Asc: req.GetAscending(),
	}, time.Now())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list prompts: %v", err)
	}
	r := &sentientpb.ListPromptsResponse{
		Total: l.Total, Agents: l.Agents, Repos: l.Repos, OlderSessions: l.OlderSessions,
		Sessions: sessionOptionsToProto(l.Sessions),
	}
	if !l.HistoryBegins.IsZero() {
		r.HistoryBegins = timestamppb.New(l.HistoryBegins)
	}
	if l.Session != nil {
		r.Session = sessionSummaryToProto(*l.Session)
	}
	for _, p := range l.Prompts {
		r.Prompts = append(r.Prompts, promptToProto(p))
	}
	return r, nil
}

// ListSessionGroups implements sentientpb.SentientServer.
func (s *Server) ListSessionGroups(ctx context.Context, req *sentientpb.ListSessionGroupsRequest) (*sentientpb.ListSessionGroupsResponse, error) {
	if req.GetSessionLimit() < 0 || req.GetSessionLimit() > query.MaxSessionLimit || req.GetPerSession() < 0 || req.GetPerSession() > 50 {
		return nil, status.Errorf(codes.InvalidArgument, "session_limit must be 0..%d and per_session 0..50", query.MaxSessionLimit)
	}
	g, err := query.ListSessionGroups(ctx, s.db, query.PromptFilter{
		Text: req.GetText(), Agent: req.GetAgent(), Repo: req.GetRepo(), Flagged: req.GetFlagged(), Limit: int(req.GetSessionLimit()),
	}, int(req.GetPerSession()), time.Now())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list session groups: %v", err)
	}
	r := &sentientpb.ListSessionGroupsResponse{
		TotalPrompts: g.TotalPrompts, TotalSessions: g.TotalSessions, Agents: g.Agents, Repos: g.Repos,
		Sessions: sessionOptionsToProto(g.Sessions), OlderSessions: g.OlderSessions,
	}
	if !g.HistoryBegins.IsZero() {
		r.HistoryBegins = timestamppb.New(g.HistoryBegins)
	}
	for _, grp := range g.Groups {
		pg := &sentientpb.SessionGroup{Session: sessionSummaryToProto(grp.Session), Matching: grp.Matching}
		for _, p := range grp.Prompts {
			pg.Prompts = append(pg.Prompts, promptToProto(p))
		}
		r.Groups = append(r.Groups, pg)
	}
	return r, nil
}

func sessionSummaryToProto(s query.SessionSummary) *sentientpb.SessionSummary {
	p := &sentientpb.SessionSummary{
		SessionId: s.ID, Agent: s.Agent, Repo: s.Repo, Prompts: s.Prompts, Actions: s.Actions, Flags: s.Flags,
	}
	if !s.Started.IsZero() {
		p.Started = timestamppb.New(s.Started)
	}
	if !s.LastActivity.IsZero() {
		p.LastActivity = timestamppb.New(s.LastActivity)
	}
	return p
}

func sessionSummaryFromProto(p *sentientpb.SessionSummary) query.SessionSummary {
	s := query.SessionSummary{
		ID: p.GetSessionId(), Agent: p.GetAgent(), Repo: p.GetRepo(), Prompts: p.GetPrompts(), Actions: p.GetActions(), Flags: p.GetFlags(),
	}
	if p.GetStarted() != nil {
		s.Started = p.GetStarted().AsTime()
	}
	if p.GetLastActivity() != nil {
		s.LastActivity = p.GetLastActivity().AsTime()
	}
	return s
}

func sessionOptionsToProto(opts []query.SessionOption) []*sentientpb.SessionOption {
	out := make([]*sentientpb.SessionOption, 0, len(opts))
	for _, o := range opts {
		out = append(out, &sentientpb.SessionOption{SessionId: o.ID, Agent: o.Agent, Repo: o.Repo})
	}
	return out
}

func sessionOptionsFromProto(opts []*sentientpb.SessionOption) []query.SessionOption {
	out := make([]query.SessionOption, 0, len(opts))
	for _, o := range opts {
		out = append(out, query.SessionOption{ID: o.GetSessionId(), Agent: o.GetAgent(), Repo: o.GetRepo()})
	}
	return out
}

// SessionDetail implements sentientpb.SentientServer.
func (s *Server) SessionDetail(ctx context.Context, req *sentientpb.SessionDetailRequest) (*sentientpb.SessionDetailResponse, error) {
	d, err := query.GetSessionDetail(ctx, s.db, req.GetSessionId(), time.Now())
	if errors.Is(err, query.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "no such session")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "session detail: %v", err)
	}
	r := &sentientpb.SessionDetailResponse{
		SessionId: d.ID, Agent: d.Agent, AgentVersion: d.AgentVersion, Repo: d.Repo, Cwd: d.Cwd,
		Running: d.Running, Actions: d.Actions, Flags: d.Flags, TotalPrompts: d.TotalPrompts, FirstPrompt: d.FirstPrompt,
	}
	if !d.HistoryBegins.IsZero() {
		r.HistoryBegins = timestamppb.New(d.HistoryBegins)
	}
	if !d.Started.IsZero() {
		r.Started = timestamppb.New(d.Started)
	}
	if !d.LastActivity.IsZero() {
		r.LastActivity = timestamppb.New(d.LastActivity)
	}
	for _, p := range d.Prompts {
		r.Prompts = append(r.Prompts, promptToProto(p))
	}
	for _, a := range d.Activity {
		r.Activity = append(r.Activity, activityToProto(a))
	}
	for _, sa := range d.Subagents {
		r.Subagents = append(r.Subagents, &sentientpb.Subagent{
			Kind: sa.Kind, Task: sa.Task, At: timestamppb.New(sa.At), PromptNumber: sa.PromptNumber,
		})
	}
	return r, nil
}

// ListPrompts fetches the prompts matching f, newest first.
func (c *Client) ListPrompts(ctx context.Context, f query.PromptFilter) (*query.PromptList, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := c.c.ListPrompts(ctx, &sentientpb.ListPromptsRequest{
		Text: f.Text, Agent: f.Agent, Repo: f.Repo, Flagged: f.Flagged, SessionId: f.SessionID, Limit: int32(f.Limit),
		Sort: f.Sort, Ascending: f.Asc,
	})
	if err != nil {
		return nil, c.callErr(err)
	}
	l := &query.PromptList{
		Total: resp.GetTotal(), Agents: resp.GetAgents(), Repos: resp.GetRepos(),
		Sessions: sessionOptionsFromProto(resp.GetSessions()), OlderSessions: resp.GetOlderSessions(),
	}
	if resp.GetHistoryBegins() != nil {
		l.HistoryBegins = resp.GetHistoryBegins().AsTime()
	}
	if resp.GetSession() != nil {
		s := sessionSummaryFromProto(resp.GetSession())
		l.Session = &s
	}
	for _, p := range resp.GetPrompts() {
		l.Prompts = append(l.Prompts, promptFromProto(p))
	}
	return l, nil
}

// ListSessionGroups fetches the sessions with a prompt matching f, newest
// first, each with its newest perSession matching prompts. f.Limit is the
// number of sessions.
func (c *Client) ListSessionGroups(ctx context.Context, f query.PromptFilter, perSession int) (*query.SessionGroups, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := c.c.ListSessionGroups(ctx, &sentientpb.ListSessionGroupsRequest{
		Text: f.Text, Agent: f.Agent, Repo: f.Repo, Flagged: f.Flagged,
		SessionLimit: int32(f.Limit), PerSession: int32(perSession),
	})
	if err != nil {
		return nil, c.callErr(err)
	}
	g := &query.SessionGroups{
		TotalPrompts: resp.GetTotalPrompts(), TotalSessions: resp.GetTotalSessions(), Agents: resp.GetAgents(), Repos: resp.GetRepos(),
		Sessions: sessionOptionsFromProto(resp.GetSessions()), OlderSessions: resp.GetOlderSessions(),
	}
	if resp.GetHistoryBegins() != nil {
		g.HistoryBegins = resp.GetHistoryBegins().AsTime()
	}
	for _, pg := range resp.GetGroups() {
		grp := query.SessionGroup{Session: sessionSummaryFromProto(pg.GetSession()), Matching: pg.GetMatching()}
		for _, p := range pg.GetPrompts() {
			grp.Prompts = append(grp.Prompts, promptFromProto(p))
		}
		g.Groups = append(g.Groups, grp)
	}
	return g, nil
}

// SessionDetail fetches one session by the agent's session id, or ErrNotFound.
func (c *Client) SessionDetail(ctx context.Context, sessionID string) (*query.SessionDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := c.c.SessionDetail(ctx, &sentientpb.SessionDetailRequest{SessionId: sessionID})
	if status.Code(err) == codes.NotFound {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, c.callErr(err)
	}
	d := &query.SessionDetail{
		ID: resp.GetSessionId(), Agent: resp.GetAgent(), AgentVersion: resp.GetAgentVersion(), Repo: resp.GetRepo(),
		Cwd: resp.GetCwd(), Running: resp.GetRunning(), Actions: resp.GetActions(), Flags: resp.GetFlags(),
		TotalPrompts: resp.GetTotalPrompts(), FirstPrompt: resp.GetFirstPrompt(),
	}
	if resp.GetHistoryBegins() != nil {
		d.HistoryBegins = resp.GetHistoryBegins().AsTime()
	}
	if resp.GetStarted() != nil {
		d.Started = resp.GetStarted().AsTime()
	}
	if resp.GetLastActivity() != nil {
		d.LastActivity = resp.GetLastActivity().AsTime()
	}
	for _, p := range resp.GetPrompts() {
		d.Prompts = append(d.Prompts, promptFromProto(p))
	}
	for _, a := range resp.GetActivity() {
		d.Activity = append(d.Activity, activityFromProto(a))
	}
	for _, sa := range resp.GetSubagents() {
		d.Subagents = append(d.Subagents, query.Subagent{
			Kind: sa.GetKind(), Task: sa.GetTask(), At: sa.GetAt().AsTime(), PromptNumber: sa.GetPromptNumber(),
		})
	}
	return d, nil
}

func promptToProto(p query.PromptRow) *sentientpb.Prompt {
	return &sentientpb.Prompt{
		Id: p.ID, Text: p.Text, Started: timestamppb.New(p.Started), Duration: durationpb.New(p.Duration),
		Running: p.Running, Agent: p.Agent, Repo: p.Repo, SessionId: p.SessionID, Actions: p.Actions, Flags: p.Flags,
	}
}

func promptFromProto(p *sentientpb.Prompt) query.PromptRow {
	return query.PromptRow{
		ID: p.GetId(), Text: p.GetText(), Started: p.GetStarted().AsTime(), Duration: p.GetDuration().AsDuration(),
		Running: p.GetRunning(), Agent: p.GetAgent(), Repo: p.GetRepo(), SessionID: p.GetSessionId(),
		Actions: p.GetActions(), Flags: p.GetFlags(),
	}
}

func activityToProto(a query.ActivityRow) *sentientpb.ActivityRow {
	return &sentientpb.ActivityRow{
		Resource: a.Resource, Action: a.Action, Count: a.Count, Summary: a.Summary,
		Repos: a.Repos, Outside: a.Outside, Added: a.Added, Removed: a.Removed, Failed: a.Failed, Hosts: a.Hosts,
	}
}

func activityFromProto(a *sentientpb.ActivityRow) query.ActivityRow {
	return query.ActivityRow{
		Resource: a.GetResource(), Action: a.GetAction(), Count: a.GetCount(), Summary: a.GetSummary(),
		Repos: a.GetRepos(), Outside: a.GetOutside(), Added: a.GetAdded(), Removed: a.GetRemoved(),
		Failed: a.GetFailed(), Hosts: a.GetHosts(),
	}
}

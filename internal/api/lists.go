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

// maxWindowDays bounds a window request (a year is far past any use).
const maxWindowDays = 3650

// PromptWindow implements sentientpb.SentientServer.
func (s *Server) PromptWindow(ctx context.Context, req *sentientpb.PromptWindowRequest) (*sentientpb.PromptWindowResponse, error) {
	if req.GetDays() < 0 || req.GetDays() > maxWindowDays || req.GetLimit() < 0 || req.GetLimit() > query.MaxWindowPrompts ||
		req.GetMinPrompts() < 0 || req.GetMinPrompts() > query.MaxWindowPrompts {
		return nil, status.Errorf(codes.InvalidArgument, "days must be 0..%d, limit and min_prompts 0..%d", maxWindowDays, query.MaxWindowPrompts)
	}
	wr := query.WindowRequest{
		Days: int(req.GetDays()), SessionID: req.GetSessionId(), Limit: int(req.GetLimit()), MinPrompts: int(req.GetMinPrompts()),
	}
	if req.GetBefore() != nil {
		wr.Before = req.GetBefore().AsTime()
	}
	w, err := query.GetPromptWindow(ctx, s.db, wr, time.Now())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "prompt window: %v", err)
	}
	r := &sentientpb.PromptWindowResponse{
		Matching: w.Matching, More: w.More, Widened: w.Widened, Agents: w.Agents, Repos: w.Repos,
		Menu: sessionOptionsToProto(w.Menu), OlderSessions: w.OlderSessions,
	}
	if !w.HistoryBegins.IsZero() {
		r.HistoryBegins = timestamppb.New(w.HistoryBegins)
	}
	if !w.Oldest.IsZero() {
		r.Oldest = timestamppb.New(w.Oldest)
	}
	for _, p := range w.Prompts {
		r.Prompts = append(r.Prompts, promptToProto(p))
	}
	for _, sum := range w.Sessions {
		r.Sessions = append(r.Sessions, sessionSummaryToProto(sum))
	}
	return r, nil
}

// PromptWindow fetches the prompts of a time window or one session.
func (c *Client) PromptWindow(ctx context.Context, req query.WindowRequest) (*query.PromptWindow, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	pr := &sentientpb.PromptWindowRequest{
		Days: int32(req.Days), SessionId: req.SessionID, Limit: int32(req.Limit), MinPrompts: int32(req.MinPrompts),
	}
	if !req.Before.IsZero() {
		pr.Before = timestamppb.New(req.Before)
	}
	resp, err := c.c.PromptWindow(ctx, pr)
	if err != nil {
		return nil, c.callErr(err)
	}
	w := &query.PromptWindow{
		Matching: resp.GetMatching(), More: resp.GetMore(), Widened: resp.GetWidened(), Agents: resp.GetAgents(), Repos: resp.GetRepos(),
		Menu: sessionOptionsFromProto(resp.GetMenu()), OlderSessions: resp.GetOlderSessions(),
	}
	if resp.GetHistoryBegins() != nil {
		w.HistoryBegins = resp.GetHistoryBegins().AsTime()
	}
	if resp.GetOldest() != nil {
		w.Oldest = resp.GetOldest().AsTime()
	}
	for _, p := range resp.GetPrompts() {
		w.Prompts = append(w.Prompts, promptFromProto(p))
	}
	for _, sum := range resp.GetSessions() {
		w.Sessions = append(w.Sessions, sessionSummaryFromProto(sum))
	}
	return w, nil
}

// SessionList implements sentientpb.SentientServer.
func (s *Server) SessionList(ctx context.Context, req *sentientpb.SessionListRequest) (*sentientpb.SessionListResponse, error) {
	if req.GetDays() < 0 || req.GetDays() > maxWindowDays {
		return nil, status.Errorf(codes.InvalidArgument, "days must be 0..%d", maxWindowDays)
	}
	l, err := query.ListSessions(ctx, s.db, int(req.GetDays()), time.Now())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "session list: %v", err)
	}
	r := &sentientpb.SessionListResponse{Total: l.Total, Agents: l.Agents, Repos: l.Repos}
	if !l.HistoryBegins.IsZero() {
		r.HistoryBegins = timestamppb.New(l.HistoryBegins)
	}
	for _, row := range l.Sessions {
		r.Sessions = append(r.Sessions, &sentientpb.SessionListRow{
			Summary: sessionSummaryToProto(row.SessionSummary), FirstPrompt: row.FirstPrompt, Running: row.Running,
		})
	}
	return r, nil
}

// SessionList fetches the sessions active in the last days days (0 = all time).
func (c *Client) SessionList(ctx context.Context, days int) (*query.SessionList, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := c.c.SessionList(ctx, &sentientpb.SessionListRequest{Days: int32(days)})
	if err != nil {
		return nil, c.callErr(err)
	}
	l := &query.SessionList{Total: resp.GetTotal(), Agents: resp.GetAgents(), Repos: resp.GetRepos()}
	if resp.GetHistoryBegins() != nil {
		l.HistoryBegins = resp.GetHistoryBegins().AsTime()
	}
	for _, row := range resp.GetSessions() {
		l.Sessions = append(l.Sessions, query.SessionRow{
			SessionSummary: sessionSummaryFromProto(row.GetSummary()), FirstPrompt: row.GetFirstPrompt(), Running: row.GetRunning(),
		})
	}
	return l, nil
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

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

// ErrNotFound means sentientd has no such record.
var ErrNotFound = errors.New("not found")

// PromptDetail implements sentientpb.SentientServer.
func (s *Server) PromptDetail(ctx context.Context, req *sentientpb.PromptDetailRequest) (*sentientpb.PromptDetailResponse, error) {
	d, err := query.GetPromptDetail(ctx, s.db, req.GetId(), time.Now())
	if errors.Is(err, query.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "no such prompt")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "prompt detail: %v", err)
	}
	return promptDetailToProto(d), nil
}

// PromptDetail fetches the detail of one prompt, or ErrNotFound.
func (c *Client) PromptDetail(ctx context.Context, id int64) (*query.PromptDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := c.c.PromptDetail(ctx, &sentientpb.PromptDetailRequest{Id: id})
	if status.Code(err) == codes.NotFound {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, c.callErr(err)
	}
	return promptDetailFromProto(resp), nil
}

func promptDetailToProto(d *query.PromptDetail) *sentientpb.PromptDetailResponse {
	p := d.Prompt
	r := &sentientpb.PromptDetailResponse{
		Prompt: &sentientpb.Prompt{
			Id: p.ID, Text: p.Text, Started: timestamppb.New(p.Started), Duration: durationpb.New(p.Duration),
			Running: p.Running, Agent: p.Agent, Repo: p.Repo, SessionId: p.SessionID, Actions: p.Actions, Flags: p.Flags,
		},
		Reply: d.Reply, RepoRoot: d.RepoRoot,
	}
	if !d.HistoryBegins.IsZero() {
		r.HistoryBegins = timestamppb.New(d.HistoryBegins)
	}
	if !d.ReplyAt.IsZero() {
		r.ReplyAt = timestamppb.New(d.ReplyAt)
	}
	for _, a := range d.Activity {
		r.Activity = append(r.Activity, &sentientpb.ActivityRow{
			Resource: a.Resource, Action: a.Action, Count: a.Count, Summary: a.Summary,
			Repos: a.Repos, Outside: a.Outside, Added: a.Added, Removed: a.Removed, Failed: a.Failed, Hosts: a.Hosts,
		})
	}
	for _, f := range d.Files {
		pf := &sentientpb.FileChange{
			Path: f.Path, Added: f.Added, Removed: f.Removed, OutsideRepo: f.Outside,
			Steps: f.Steps, By: f.By, Action: f.Action, First: timestamppb.New(f.First),
		}
		for _, l := range f.Diff {
			pf.Diff = append(pf.Diff, &sentientpb.DiffLine{Kind: l.Kind, Text: l.Text})
		}
		r.Files = append(r.Files, pf)
	}
	for _, s := range d.Steps {
		ps := &sentientpb.Step{
			N: s.N, Offset: durationpb.New(s.Offset), Resource: s.Resource, Action: s.Action, Path: s.Path,
			Failed: s.Failed, Evidence: s.Evidence, CallId: s.CallID, CallCommand: s.CallCommand, Builtin: s.Builtin, ActionId: s.ActionID,
			Alert: s.Alert, Unreported: s.Unreported, Unverified: s.Unverified,
		}
		r.Steps = append(r.Steps, ps)
	}
	r.Counts = &sentientpb.EvidenceCounts{
		Watched: d.Counts.Watched, AgentLog: d.Counts.AgentLog, KernelLog: d.Counts.KernelLog,
		Unverified: d.Counts.Unverified, Unreported: d.Counts.Unreported, Background: d.Counts.Background,
		AgentId: d.Counts.AgentID, Confirmed: d.Counts.Confirmed, NotChecked: d.Counts.NotChecked, Pending: d.Counts.Pending,
	}
	return r
}

func promptDetailFromProto(r *sentientpb.PromptDetailResponse) *query.PromptDetail {
	p := r.GetPrompt()
	d := &query.PromptDetail{
		Prompt: query.PromptRow{
			ID: p.GetId(), Text: p.GetText(), Started: p.GetStarted().AsTime(), Duration: p.GetDuration().AsDuration(),
			Running: p.GetRunning(), Agent: p.GetAgent(), Repo: p.GetRepo(), SessionID: p.GetSessionId(),
			Actions: p.GetActions(), Flags: p.GetFlags(),
		},
		Reply: r.GetReply(), RepoRoot: r.GetRepoRoot(),
	}
	if r.GetHistoryBegins() != nil {
		d.HistoryBegins = r.GetHistoryBegins().AsTime()
	}
	if r.GetReplyAt() != nil {
		d.ReplyAt = r.GetReplyAt().AsTime()
	}
	for _, a := range r.GetActivity() {
		d.Activity = append(d.Activity, query.ActivityRow{
			Resource: a.GetResource(), Action: a.GetAction(), Count: a.GetCount(), Summary: a.GetSummary(),
			Repos: a.GetRepos(), Outside: a.GetOutside(), Added: a.GetAdded(), Removed: a.GetRemoved(),
			Failed: a.GetFailed(), Hosts: a.GetHosts(),
		})
	}
	for _, f := range r.GetFiles() {
		qf := query.FileChange{
			Path: f.GetPath(), Added: f.GetAdded(), Removed: f.GetRemoved(), Outside: f.GetOutsideRepo(),
			Steps: f.GetSteps(), By: f.GetBy(), Action: f.GetAction(), First: f.GetFirst().AsTime(),
		}
		for _, l := range f.GetDiff() {
			qf.Diff = append(qf.Diff, query.DiffLine{Kind: l.GetKind(), Text: l.GetText()})
		}
		d.Files = append(d.Files, qf)
	}
	for _, s := range r.GetSteps() {
		qs := query.Step{
			N: s.GetN(), Offset: s.GetOffset().AsDuration(), Resource: s.GetResource(), Action: s.GetAction(),
			Path: s.GetPath(), Failed: s.GetFailed(), Evidence: s.GetEvidence(), CallID: s.GetCallId(),
			CallCommand: s.GetCallCommand(), Builtin: s.GetBuiltin(), ActionID: s.GetActionId(),
			Alert: s.GetAlert(), Unreported: s.GetUnreported(), Unverified: s.GetUnverified(),
		}
		d.Steps = append(d.Steps, qs)
	}
	c := r.GetCounts()
	d.Counts = query.EvidenceCounts{
		Watched: c.GetWatched(), AgentLog: c.GetAgentLog(), KernelLog: c.GetKernelLog(),
		Unverified: c.GetUnverified(), Unreported: c.GetUnreported(), Background: c.GetBackground(),
		AgentID: c.GetAgentId(), Confirmed: c.GetConfirmed(), NotChecked: c.GetNotChecked(), Pending: c.GetPending(),
	}
	return d
}

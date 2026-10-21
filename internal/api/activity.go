package api

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/nosaka4i/sentient/internal/api/sentientpb"
	"github.com/nosaka4i/sentient/internal/query"
)

// ActivityList implements sentientpb.SentientServer.
func (s *Server) ActivityList(ctx context.Context, req *sentientpb.ActivityListRequest) (*sentientpb.ActivityListResponse, error) {
	if req.GetDays() < 1 || req.GetDays() > maxWindowDays || req.GetLimit() < 0 || req.GetLimit() > query.MaxWindowPrompts {
		return nil, status.Errorf(codes.InvalidArgument, "days must be 1..%d and limit 0..%d", maxWindowDays, query.MaxWindowPrompts)
	}
	a, err := query.GetActivity(ctx, s.db, query.ActivityRequest{
		Days: int(req.GetDays()), Resource: req.GetResource(), Action: req.GetAction(), FailedOnly: req.GetFailedOnly(), Limit: int(req.GetLimit()), Search: req.GetQuery(),
	}, time.Now())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "activity: %v", err)
	}
	r := &sentientpb.ActivityListResponse{
		From: timestamppb.New(a.From), To: timestamppb.New(a.To),
		Resource: a.Resource, Action: a.Action, EventsTotal: a.EventsTotal,
	}
	if !a.HistoryBegins.IsZero() {
		r.HistoryBegins = timestamppb.New(a.HistoryBegins)
	}
	for _, row := range a.Rows {
		r.Rows = append(r.Rows, activityToProto(row))
	}
	for _, e := range a.Events {
		r.Events = append(r.Events, &sentientpb.ActivityEvent{
			At: timestamppb.New(e.At), What: e.What, Failed: e.Failed, LinesAdded: e.Added, LinesRemoved: e.Removed,
			PromptId: e.PromptID, PromptText: e.PromptText, Agent: e.Agent, Repo: e.Repo, SessionId: e.SessionID,
		})
	}
	return r, nil
}

// ActivityList fetches the activity counts of the last req.Days days and the
// events of one kind.
func (c *Client) ActivityList(ctx context.Context, req query.ActivityRequest) (*query.Activity, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.c.ActivityList(ctx, &sentientpb.ActivityListRequest{
		Days: int32(req.Days), Resource: req.Resource, Action: req.Action, FailedOnly: req.FailedOnly, Limit: int32(req.Limit), Query: req.Search,
	})
	if err != nil {
		return nil, c.callErr(err)
	}
	a := &query.Activity{Resource: resp.GetResource(), Action: resp.GetAction(), EventsTotal: resp.GetEventsTotal()}
	if resp.GetHistoryBegins() != nil {
		a.HistoryBegins = resp.GetHistoryBegins().AsTime()
	}
	if resp.GetFrom() != nil {
		a.From = resp.GetFrom().AsTime()
	}
	if resp.GetTo() != nil {
		a.To = resp.GetTo().AsTime()
	}
	for _, row := range resp.GetRows() {
		a.Rows = append(a.Rows, activityFromProto(row))
	}
	for _, e := range resp.GetEvents() {
		a.Events = append(a.Events, query.ActivityEvent{
			At: e.GetAt().AsTime(), What: e.GetWhat(), Failed: e.GetFailed(), Added: e.GetLinesAdded(), Removed: e.GetLinesRemoved(),
			PromptID: e.GetPromptId(), PromptText: e.GetPromptText(), Agent: e.GetAgent(), Repo: e.GetRepo(), SessionID: e.GetSessionId(),
		})
	}
	return a, nil
}

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

func validStatus(s string) bool   { return s == "" || s == query.AlertOpen || s == query.AlertReviewed }
func validSeverity(s string) bool { return s == "" || s == "high" || s == "medium" || s == "low" }

// AlertList implements sentientpb.SentientServer.
func (s *Server) AlertList(ctx context.Context, req *sentientpb.AlertListRequest) (*sentientpb.AlertListResponse, error) {
	if !validStatus(req.GetStatus()) || !validSeverity(req.GetSeverity()) || req.GetLimit() < 0 || req.GetLimit() > query.MaxListedAlerts {
		return nil, status.Errorf(codes.InvalidArgument, "status open|reviewed, severity high|medium|low, limit 0..%d", query.MaxListedAlerts)
	}
	l, err := query.ListAlerts(ctx, s.db, query.AlertFilter{Status: req.GetStatus(), Severity: req.GetSeverity(), Limit: int(req.GetLimit())})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "alert list: %v", err)
	}
	r := &sentientpb.AlertListResponse{
		Open: l.Counts.Open, Reviewed: l.Counts.Reviewed,
		OpenHigh: l.Counts.OpenHigh, OpenMedium: l.Counts.OpenMedium, OpenLow: l.Counts.OpenLow,
	}
	if !l.HistoryBegins.IsZero() {
		r.HistoryBegins = timestamppb.New(l.HistoryBegins)
	}
	for _, a := range l.Alerts {
		r.Alerts = append(r.Alerts, alertToProto(a))
	}
	return r, nil
}

// AlertDetail implements sentientpb.SentientServer.
func (s *Server) AlertDetail(ctx context.Context, req *sentientpb.AlertDetailRequest) (*sentientpb.AlertDetailResponse, error) {
	d, err := query.GetAlert(ctx, s.db, req.GetId())
	if errors.Is(err, query.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "no such alert")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "alert detail: %v", err)
	}
	r := &sentientpb.AlertDetailResponse{Alert: alertToProto(d.Alert), RawRecord: d.RawRecord}
	if !d.HistoryBegins.IsZero() {
		r.HistoryBegins = timestamppb.New(d.HistoryBegins)
	}
	return r, nil
}

// ReviewAlert implements sentientpb.SentientServer.
func (s *Server) ReviewAlert(ctx context.Context, req *sentientpb.ReviewAlertRequest) (*sentientpb.ReviewAlertResponse, error) {
	err := query.ReviewAlert(ctx, s.db, req.GetId(), req.GetReviewed(), time.Now())
	if errors.Is(err, query.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "no such alert")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "review alert: %v", err)
	}
	return &sentientpb.ReviewAlertResponse{}, nil
}

// AlertList fetches alerts matching f and the counts of all alerts.
func (c *Client) AlertList(ctx context.Context, f query.AlertFilter) (*query.AlertList, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := c.c.AlertList(ctx, &sentientpb.AlertListRequest{Status: f.Status, Severity: f.Severity, Limit: int32(f.Limit)})
	if err != nil {
		return nil, c.callErr(err)
	}
	l := &query.AlertList{Counts: query.AlertCounts{
		Open: resp.GetOpen(), Reviewed: resp.GetReviewed(),
		OpenHigh: resp.GetOpenHigh(), OpenMedium: resp.GetOpenMedium(), OpenLow: resp.GetOpenLow(),
	}}
	if resp.GetHistoryBegins() != nil {
		l.HistoryBegins = resp.GetHistoryBegins().AsTime()
	}
	for _, a := range resp.GetAlerts() {
		l.Alerts = append(l.Alerts, alertFromProto(a))
	}
	return l, nil
}

// AlertDetail fetches one alert, or ErrNotFound.
func (c *Client) AlertDetail(ctx context.Context, id int64) (*query.AlertDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := c.c.AlertDetail(ctx, &sentientpb.AlertDetailRequest{Id: id})
	if status.Code(err) == codes.NotFound {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, c.callErr(err)
	}
	d := &query.AlertDetail{Alert: alertFromProto(resp.GetAlert()), RawRecord: resp.GetRawRecord()}
	if resp.GetHistoryBegins() != nil {
		d.HistoryBegins = resp.GetHistoryBegins().AsTime()
	}
	return d, nil
}

// ReviewAlert marks an alert reviewed (true) or open again (false), or
// returns ErrNotFound.
func (c *Client) ReviewAlert(ctx context.Context, id int64, reviewed bool) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := c.c.ReviewAlert(ctx, &sentientpb.ReviewAlertRequest{Id: id, Reviewed: reviewed})
	if status.Code(err) == codes.NotFound {
		return ErrNotFound
	}
	if err != nil {
		return c.callErr(err)
	}
	return nil
}

func alertToProto(a query.AlertRow) *sentientpb.Alert {
	p := &sentientpb.Alert{
		Id: a.ID, At: timestamppb.New(a.At), Severity: a.Severity, Rule: a.Rule, Status: a.Status,
		ActionId: a.ActionID, Resource: a.Resource, Action: a.Action, Path: a.Path, Summary: a.Summary,
		PromptId: a.PromptID, PromptText: a.PromptText, Agent: a.Agent, Repo: a.Repo, SessionId: a.SessionID,
	}
	if !a.ReviewedAt.IsZero() {
		p.ReviewedAt = timestamppb.New(a.ReviewedAt)
	}
	return p
}

func alertFromProto(p *sentientpb.Alert) query.AlertRow {
	a := query.AlertRow{
		ID: p.GetId(), At: p.GetAt().AsTime(), Severity: p.GetSeverity(), Rule: p.GetRule(), Status: p.GetStatus(),
		ActionID: p.GetActionId(), Resource: p.GetResource(), Action: p.GetAction(), Path: p.GetPath(), Summary: p.GetSummary(),
		PromptID: p.GetPromptId(), PromptText: p.GetPromptText(), Agent: p.GetAgent(), Repo: p.GetRepo(), SessionID: p.GetSessionId(),
	}
	if p.GetReviewedAt() != nil {
		a.ReviewedAt = p.GetReviewedAt().AsTime()
	}
	return a
}

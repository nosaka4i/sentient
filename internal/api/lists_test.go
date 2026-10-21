package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/nosaka4i/sentient/internal/api/sentientpb"
	"github.com/nosaka4i/sentient/internal/query"
)

func TestPromptWindowRoundTrip(t *testing.T) {
	sock := startServer(t)
	c, err := Dial(sock, "test client")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	// The seeded prompt is from 2026, long before "now": a 3-day window is
	// quiet, so it is widened to the newest prompts, which is everything.
	w, err := c.PromptWindow(ctx, query.WindowRequest{Days: 3})
	if err != nil {
		t.Fatal(err)
	}
	if w.Matching != 1 || len(w.Prompts) != 1 || w.Prompts[0].Text != "hi" || w.Prompts[0].SessionID != "s" || !w.Widened || w.More ||
		w.HistoryBegins.IsZero() || w.Oldest.IsZero() {
		t.Fatalf("window = %+v", w)
	}
	if len(w.Sessions) != 1 || w.Sessions[0].ID != "s" || w.Sessions[0].Agent != "claude" || w.Sessions[0].Repo != "w" || w.Sessions[0].Prompts != 1 {
		t.Errorf("sessions = %+v", w.Sessions)
	}
	if len(w.Menu) != 1 || w.Menu[0].ID != "s" || w.OlderSessions != 0 || len(w.Agents) != 1 || len(w.Repos) != 1 {
		t.Errorf("options: menu %+v older %d agents %v repos %v", w.Menu, w.OlderSessions, w.Agents, w.Repos)
	}

	// One session, and paging older than a point in time.
	w, err = c.PromptWindow(ctx, query.WindowRequest{SessionID: "s"})
	if err != nil || len(w.Prompts) != 1 {
		t.Errorf("session window = %+v, %v", w, err)
	}
	w, err = c.PromptWindow(ctx, query.WindowRequest{Days: 0, Before: time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)})
	if err != nil || len(w.Prompts) != 0 || w.Matching != 0 {
		t.Errorf("before the only prompt: %+v, %v", w, err)
	}
	if w, err = c.PromptWindow(ctx, query.WindowRequest{SessionID: "nope"}); err != nil || len(w.Prompts) != 0 {
		t.Errorf("unknown session: %+v, %v", w, err)
	}
}

func TestSessionListRoundTrip(t *testing.T) {
	sock := startServer(t)
	c, err := Dial(sock, "test client")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	// the seeded session is from 2026, so all time has it and "the last day" doesn't
	l, err := c.SessionList(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if l.Total != 1 || len(l.Sessions) != 1 || l.HistoryBegins.IsZero() || len(l.Agents) != 1 || len(l.Repos) != 1 {
		t.Fatalf("list = %+v", l)
	}
	if s := l.Sessions[0]; s.ID != "s" || s.Agent != "claude" || s.Repo != "w" || s.Prompts != 1 || s.FirstPrompt != "hi" || s.Running {
		t.Errorf("session = %+v", s)
	}
	if day, err := c.SessionList(ctx, 1); err != nil || len(day.Sessions) != 0 || day.Total != 1 {
		t.Errorf("last day = %+v, %v", day, err)
	}
	for _, days := range []int32{-1, maxWindowDays + 1} {
		if _, err := NewServer(nil).SessionList(ctx, &sentientpb.SessionListRequest{Days: days}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("days %d: %v", days, err)
		}
	}
}

func TestActivityListRoundTrip(t *testing.T) {
	sock := startServer(t)
	c, err := Dial(sock, "test client")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	// the seeded data is from 2026, so a recent window is quiet: all 22 kinds with zero counts
	a, err := c.ActivityList(ctx, query.ActivityRequest{Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Rows) != 22 || a.Rows[0].Resource != "file" || a.From.IsZero() || a.To.IsZero() || a.HistoryBegins.IsZero() {
		t.Fatalf("activity = %+v", a)
	}
	if a.Resource != "" || len(a.Events) != 0 || a.EventsTotal != 0 {
		t.Errorf("a quiet window lists nothing: %s/%s %d events", a.Resource, a.Action, len(a.Events))
	}
	// asking for a kind echoes it back
	k, err := c.ActivityList(ctx, query.ActivityRequest{Days: 30000 / 100, Resource: "prompt", Action: "request", Limit: 5})
	if err != nil || k.Resource != "prompt" {
		t.Errorf("kind = %+v, %v", k, err)
	}
	for _, req := range []*sentientpb.ActivityListRequest{{Days: -1}, {Days: maxWindowDays + 1}, {Days: 1, Limit: -1}, {Days: 1, Limit: query.MaxWindowPrompts + 1}} {
		if _, err := NewServer(nil).ActivityList(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%+v: %v", req, err)
		}
	}
}

func TestAlertsRoundTrip(t *testing.T) {
	sock := startServer(t)
	c, err := Dial(sock, "test client")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	l, err := c.AlertList(ctx, query.AlertFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if l.Counts.Open != 1 || l.Counts.Reviewed != 0 || l.Counts.OpenMedium != 1 || len(l.Alerts) != 1 || l.HistoryBegins.IsZero() {
		t.Fatalf("list = %+v", l)
	}
	a := l.Alerts[0]
	if a.Severity != "medium" || a.Rule != "outside-read" || a.Status != "open" || a.Resource != "file" || a.Action != "read" || a.Path != "/w/a" ||
		a.SessionID != "s" || a.Agent != "claude" || a.Repo != "w" || a.At.IsZero() || !a.ReviewedAt.IsZero() {
		t.Errorf("alert = %+v", a)
	}
	if hi, _ := c.AlertList(ctx, query.AlertFilter{Severity: "high"}); len(hi.Alerts) != 0 || hi.Counts.Open != 1 {
		t.Errorf("high filter = %+v", hi)
	}

	d, err := c.AlertDetail(ctx, a.ID)
	if err != nil || d.Alert.ID != a.ID || d.HistoryBegins.IsZero() {
		t.Fatalf("detail = %+v, %v", d, err)
	}
	if _, err := c.AlertDetail(ctx, 99999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown alert = %v", err)
	}

	if err := c.ReviewAlert(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	l, _ = c.AlertList(ctx, query.AlertFilter{Status: "reviewed"})
	if len(l.Alerts) != 1 || l.Alerts[0].ReviewedAt.IsZero() || l.Counts.Open != 0 || l.Counts.Reviewed != 1 {
		t.Errorf("after review = %+v", l)
	}
	if err := c.ReviewAlert(ctx, a.ID, false); err != nil {
		t.Fatal(err)
	}
	if l, _ = c.AlertList(ctx, query.AlertFilter{Status: "open"}); len(l.Alerts) != 1 {
		t.Errorf("reopened = %+v", l)
	}
	if err := c.ReviewAlert(ctx, 99999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("review unknown = %v", err)
	}

	srv := NewServer(nil)
	for _, req := range []*sentientpb.AlertListRequest{{Status: "bogus"}, {Severity: "bogus"}, {Limit: -1}, {Limit: query.MaxListedAlerts + 1}} {
		if _, err := srv.AlertList(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%+v: %v", req, err)
		}
	}
}

func TestPromptWindowRejectsBadRequests(t *testing.T) {
	srv := NewServer(nil)
	for _, req := range []*sentientpb.PromptWindowRequest{
		{Days: -1},
		{Days: maxWindowDays + 1},
		{Limit: query.MaxWindowPrompts + 1},
		{MinPrompts: query.MaxWindowPrompts + 1},
		{Limit: -5},
	} {
		if _, err := srv.PromptWindow(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%+v: err = %v, want InvalidArgument", req, err)
		}
	}
}

func TestSessionDetailRoundTrip(t *testing.T) {
	sock := startServer(t)
	c, err := Dial(sock, "test client")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	d, err := c.SessionDetail(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "s" || d.Agent != "claude" || d.Repo != "w" || d.Cwd != "/w" || len(d.Prompts) != 1 || d.TotalPrompts != 1 || d.FirstPrompt != "hi" ||
		len(d.Activity) != len(query.StatusRows) || d.HistoryBegins.IsZero() || d.Started.IsZero() {
		t.Errorf("session = %+v", d)
	}
	if _, err := c.SessionDetail(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing session: err = %v, want ErrNotFound", err)
	}
}

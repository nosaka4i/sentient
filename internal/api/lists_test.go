package api

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/nosaka4i/sentient/internal/api/sentientpb"
	"github.com/nosaka4i/sentient/internal/query"
)

func TestListPromptsAndSessionRoundTrip(t *testing.T) {
	sock := startServer(t)
	c, err := Dial(sock, "test client")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	l, err := c.ListPrompts(ctx, query.PromptFilter{Text: "HI"})
	if err != nil {
		t.Fatal(err)
	}
	if l.Total != 1 || len(l.Prompts) != 1 || l.Prompts[0].Text != "hi" || l.Prompts[0].SessionID != "s" ||
		len(l.Agents) != 1 || l.Agents[0] != "claude" || len(l.Repos) != 1 || l.Repos[0] != "w" || l.HistoryBegins.IsZero() {
		t.Errorf("list = %+v", l)
	}
	if l, err := c.ListPrompts(ctx, query.PromptFilter{Agent: "codex"}); err != nil || l.Total != 0 || len(l.Prompts) != 0 {
		t.Errorf("filtered list = %+v, %v", l, err)
	}

	d, err := c.SessionDetail(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "s" || d.Agent != "claude" || d.Repo != "w" || d.Cwd != "/w" || len(d.Prompts) != 1 ||
		len(d.Activity) != len(query.StatusRows) || d.HistoryBegins.IsZero() || d.Started.IsZero() {
		t.Errorf("session = %+v", d)
	}
	if _, err := c.SessionDetail(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing session: err = %v, want ErrNotFound", err)
	}
}

func TestSessionGroupsAndSessionFilterRoundTrip(t *testing.T) {
	sock := startServer(t)
	c, err := Dial(sock, "test client")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	g, err := c.ListSessionGroups(ctx, query.PromptFilter{Limit: 5}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if g.TotalPrompts != 1 || g.TotalSessions != 1 || len(g.Groups) != 1 || g.HistoryBegins.IsZero() {
		t.Fatalf("groups = %+v", g)
	}
	grp := g.Groups[0]
	if grp.Session.ID != "s" || grp.Session.Agent != "claude" || grp.Session.Repo != "w" || grp.Session.Prompts != 1 ||
		grp.Matching != 1 || len(grp.Prompts) != 1 || grp.Prompts[0].Text != "hi" {
		t.Errorf("group = %+v", grp)
	}
	if len(g.Sessions) != 1 || g.Sessions[0].ID != "s" || g.Sessions[0].Repo != "w" || g.OlderSessions != 0 || len(g.Agents) != 1 || len(g.Repos) != 1 {
		t.Errorf("menu/options = %+v older %d agents %v repos %v", g.Sessions, g.OlderSessions, g.Agents, g.Repos)
	}

	l, err := c.ListPrompts(ctx, query.PromptFilter{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if l.Session == nil || l.Session.ID != "s" || l.Session.Prompts != 1 || l.Total != 1 || len(l.Sessions) != 1 {
		t.Errorf("session list = %+v session %+v", l, l.Session)
	}
	if l, err := c.ListPrompts(ctx, query.PromptFilter{SessionID: "nope"}); err != nil || l.Session != nil || l.Total != 0 {
		t.Errorf("unknown session: %+v, %v", l, err)
	}
}

func TestListSessionGroupsRejectsBadLimits(t *testing.T) {
	srv := NewServer(nil)
	for _, req := range []*sentientpb.ListSessionGroupsRequest{
		{SessionLimit: query.MaxSessionLimit + 1},
		{PerSession: 51},
		{SessionLimit: -1},
	} {
		if _, err := srv.ListSessionGroups(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%+v: err = %v, want InvalidArgument", req, err)
		}
	}
}

func TestListPromptsRejectsBadLimit(t *testing.T) {
	srv := NewServer(nil)
	_, err := srv.ListPrompts(context.Background(), &sentientpb.ListPromptsRequest{Limit: query.MaxListLimit + 1})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("err = %v, want InvalidArgument", err)
	}
}

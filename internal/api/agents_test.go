package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/query"
)

func TestAgentsRoundTrip(t *testing.T) {
	sock := startServer(t)
	c, err := Dial(sock, "test client")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	list, err := c.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "root" || list[0].Agent != "claude" || list[0].Repo != "w" || list[0].Version != "2.1.9" ||
		!list[0].Running || !list[0].Stopped.IsZero() || list[0].Background != 2 {
		t.Fatalf("agents = %+v", list)
	}

	d, err := c.AgentDetail(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "root" || d.Dir != "/w" || len(d.Sessions) != 1 || d.Sessions[0].ID != "s" || len(d.Groups) != 2 {
		t.Fatalf("detail = %+v", d)
	}
	g := d.Groups[0]
	if g.Count != 1 || len(g.Latest) != 1 || g.Latest[0].Command == "" || g.First.IsZero() || g.Last.IsZero() {
		t.Errorf("group = %+v", g)
	}
	if _, err := c.AgentDetail(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown agent: err = %v, want ErrNotFound", err)
	}

	tl, err := c.AgentTimeline(ctx, "root", query.TimelineFilter{Text: "config --get"})
	if err != nil {
		t.Fatal(err)
	}
	if tl.Total != 1 || len(tl.Runs) != 1 || tl.Runs[0].Key != "git config --get" {
		t.Errorf("filtered timeline = %+v", tl)
	}
	from := time.Date(2026, 10, 17, 9, 2, 30, 0, time.UTC)
	tl, err = c.AgentTimeline(ctx, "root", query.TimelineFilter{From: from, To: from.Add(time.Hour), Offset: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if tl.Total != 1 || tl.Runs[0].Key != "git config --get" {
		t.Errorf("windowed timeline = %+v", tl)
	}
	if _, err := c.AgentTimeline(ctx, "nope", query.TimelineFilter{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown agent timeline: err = %v, want ErrNotFound", err)
	}
}

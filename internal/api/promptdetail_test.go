package api

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/query"
)

func TestPromptDetailRoundTrip(t *testing.T) {
	sock := startServer(t)
	c, err := Dial(sock, "test client")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	d, err := c.PromptDetail(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if d.Prompt.Text != "hi" || d.Prompt.Agent != "claude" || d.Prompt.Repo != "w" || d.Prompt.SessionID != "s" {
		t.Errorf("prompt = %+v", d.Prompt)
	}
	if len(d.Activity) != len(query.StatusRows) || d.Activity[0].Resource != "prompt" || d.Activity[0].Count != 1 {
		t.Errorf("activity = %+v", d.Activity)
	}
	if len(d.Steps) != 1 || d.Steps[0].Resource != "prompt" {
		t.Errorf("steps = %+v (the prompt itself is step 1)", d.Steps)
	}
	if d.HistoryBegins.IsZero() {
		t.Error("HistoryBegins not carried")
	}

	if _, err := c.PromptDetail(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing prompt: err = %v, want ErrNotFound", err)
	}
}

func TestPromptDetailConversion(t *testing.T) {
	at := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	in := &query.PromptDetail{
		HistoryBegins: at.Add(-time.Hour),
		Prompt: query.PromptRow{
			ID: 7, Text: "t", Started: at, Duration: 3 * time.Minute, Running: true,
			Agent: "claude", Repo: "r", SessionID: "sid", Actions: 4, Flags: 1,
		},
		Activity: []query.ActivityRow{{Resource: "file", Action: "update", Count: 2, Summary: "+3 −1", Added: 3, Removed: 1, Failed: 1, Hosts: "h", Repos: 1, Outside: 2}},
		Files: []query.FileChange{{
			Path: "/a", Action: "update", Added: 3, Removed: 1, Outside: true, Steps: []int64{2, 5}, By: "Edit", First: at,
			Diff: []query.DiffLine{{Kind: "h", Text: "Edit"}, {Kind: "a", Text: "x"}},
		}},
		Steps: []query.Step{
			{N: 1, Resource: "prompt", Action: "request", Path: "t", Evidence: "log"},
			{N: 2, Offset: 5 * time.Second, Resource: "command", Action: "execute", Path: "cd x", Failed: true, Evidence: "log", CallID: "b1", CallCommand: "cd x && ls", Builtin: true},
		},
		Reply:    "done",
		ReplyAt:  at.Add(time.Minute),
		RepoRoot: "/w/r",
	}
	out := promptDetailFromProto(promptDetailToProto(in))
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip changed the detail:\n in  %+v\n out %+v", in, out)
	}

	// Unset times stay unset.
	empty := promptDetailFromProto(promptDetailToProto(&query.PromptDetail{Prompt: query.PromptRow{Started: at}}))
	if !empty.HistoryBegins.IsZero() || !empty.ReplyAt.IsZero() {
		t.Errorf("zero times became %v / %v", empty.HistoryBegins, empty.ReplyAt)
	}
}

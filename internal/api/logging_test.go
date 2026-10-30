package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The Health page counts failed requests by code; the log has to say which one.
func TestFailedRequestsAreLogged(t *testing.T) {
	var buf bytes.Buffer
	s := NewServer(nil)
	s.SetLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	run := func(err error) {
		_, _ = s.logInterceptor()(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/sentient.Sentient/AgentDetail"},
			func(context.Context, any) (any, error) { return nil, err })
	}

	for _, err := range []error{
		status.Error(codes.Internal, "agent: database is locked"),
		status.Error(codes.DeadlineExceeded, "slow"),
		status.Error(codes.Unavailable, "closed"),
		errors.New("a plain error is Unknown"),
	} {
		buf.Reset()
		run(err)
		out := buf.String()
		if !strings.Contains(out, `msg="api request failed"`) || !strings.Contains(out, "method=AgentDetail") {
			t.Errorf("%v not logged: %q", err, out)
		}
	}
	if buf.Reset(); true {
		run(status.Error(codes.Internal, "agent: database is locked"))
		if !strings.Contains(buf.String(), "database is locked") || !strings.Contains(buf.String(), "code=Internal") {
			t.Errorf("the log lacks the reason or the code: %q", buf.String())
		}
	}

	// a caller's mistake, a cancelled request and success are not logged
	for _, err := range []error{status.Error(codes.NotFound, "no such agent"), status.Error(codes.InvalidArgument, "bad"), status.Error(codes.Canceled, "gone"), nil} {
		buf.Reset()
		run(err)
		if buf.Len() != 0 {
			t.Errorf("%v logged: %q", err, buf.String())
		}
	}

	// no logger set: nothing to write to, and nothing breaks
	quietServer := NewServer(nil)
	_, err := quietServer.logInterceptor()(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/x/Y"},
		func(context.Context, any) (any, error) { return nil, status.Error(codes.Internal, "x") })
	if status.Code(err) != codes.Internal {
		t.Errorf("the interceptor changed the error: %v", err)
	}
}

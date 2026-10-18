package api

import (
	"os/user"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func timestampOf(t time.Time) *timestamppb.Timestamp { return timestamppb.New(t) }

// currentGroup returns the name of the test process's primary group.
func currentGroup(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		return ""
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		return ""
	}
	return g.Name
}

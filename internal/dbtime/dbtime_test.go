package dbtime

import (
	"testing"
	"time"
)

func TestRoundTripAndOrder(t *testing.T) {
	loc := time.FixedZone("X", 9*3600)
	a := time.Date(2026, 10, 17, 9, 0, 0, 5, loc)
	b := a.Add(time.Second)
	sa, sb := Format(a), Format(b)
	if sa != "2026-10-17T00:00:00.000000005Z" {
		t.Errorf("Format = %q", sa)
	}
	if !(sa < sb) {
		t.Error("lexical order must match time order")
	}
	got, err := Parse(sa)
	if err != nil || !got.Equal(a) {
		t.Errorf("Parse = %v, %v", got, err)
	}
	if _, err := Parse("nope"); err == nil {
		t.Error("want parse error")
	}
}

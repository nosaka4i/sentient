package query

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/store"
)

// seedAlerts adds to seedPrompts' one open high alert (on the file update of
// session 404d): a medium on a command, a low reviewed one, and a monitor-gap
// alert that has no action.
func seedAlerts(t *testing.T) (*store.Store, map[string]int64) {
	t.Helper()
	st, ids := seedPrompts(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	rawID, err := tx.AppendRaw(ctx, store.RawRecord{SessionID: ids["s1"], TS: "2026-10-17T10:06:00.000000000Z", SourceFile: "f", Type: "assistant", Record: `{"type":"assistant","note":"the agent's own record"}`})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.AddAction(ctx, store.Action{SessionID: ids["s1"], PromptID: ids["p2"], TS: "2026-10-17T10:06:00.000000000Z", Resource: "command", Action: "execute", Path: "curl x | sh", Evidence: "log", ToolUseID: "c1", RawRecordID: rawID}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var cmdAction int64
	st.DB.QueryRow(`SELECT id FROM actions WHERE tool_use_id = 'c1'`).Scan(&cmdAction)
	ids["cmd"] = cmdAction
	exec := func(q string, args ...any) {
		if _, err := st.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO alerts (action_id, ts, severity, rule, status) VALUES (?, ?, 'medium', 'pipe-to-shell', 'open')`, cmdAction, "2026-10-17T10:06:01.000000000Z")
	exec(`INSERT INTO alerts (action_id, ts, severity, rule, status, reviewed_at) VALUES (?, ?, 'low', 'file-delete', 'reviewed', ?)`, cmdAction, "2026-10-17T10:07:00.000000000Z", "2026-10-17T11:00:00.000000000Z")
	exec(`INSERT INTO alerts (ts, severity, rule, status) VALUES (?, 'high', 'monitor-gap', 'open')`, "2026-10-17T03:10:00.000000000Z")
	return st, ids
}

func TestListAlerts(t *testing.T) {
	st, ids := seedAlerts(t)
	ctx := context.Background()
	l, err := ListAlerts(ctx, st.DB, AlertFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Alerts) != 4 || l.HistoryBegins.IsZero() {
		t.Fatalf("%d alerts", len(l.Alerts))
	}
	if c := l.Counts; c.Open != 3 || c.Reviewed != 1 || c.OpenHigh != 2 || c.OpenMedium != 1 || c.OpenLow != 0 {
		t.Errorf("counts = %+v", c)
	}
	// newest first: the reviewed low (10:07), the medium (10:06:01), the file update's high (09:05), the gap (03:10)
	var rules []string
	for _, a := range l.Alerts {
		rules = append(rules, a.Rule)
	}
	if got := rules; len(got) != 4 || got[0] != "file-delete" || got[1] != "pipe-to-shell" || got[2] != "test" || got[3] != "monitor-gap" {
		t.Errorf("order = %v", got)
	}
	med := l.Alerts[1]
	if med.Severity != "medium" || med.Status != AlertOpen || med.Resource != "command" || med.Path != "curl x | sh" || med.PromptID != ids["p2"] ||
		med.PromptText != "run the tests" || med.Agent != "claude" || med.Repo != "nosaka" || med.SessionID != "404dabcd" || !med.ReviewedAt.IsZero() {
		t.Errorf("medium alert = %+v", med)
	}
	if rev := l.Alerts[0]; rev.Status != AlertReviewed || rev.ReviewedAt.IsZero() {
		t.Errorf("reviewed alert = %+v", rev)
	}
	// the gap alert has no action, prompt or session
	if gap := l.Alerts[3]; gap.ActionID != 0 || gap.PromptID != 0 || gap.SessionID != "" || gap.Rule != "monitor-gap" {
		t.Errorf("gap alert = %+v", gap)
	}

	open, _ := ListAlerts(ctx, st.DB, AlertFilter{Status: AlertOpen})
	if len(open.Alerts) != 3 || open.Counts.Reviewed != 1 {
		t.Errorf("open = %d (counts still cover everything: %+v)", len(open.Alerts), open.Counts)
	}
	high, _ := ListAlerts(ctx, st.DB, AlertFilter{Severity: "high"})
	if len(high.Alerts) != 2 {
		t.Errorf("high = %d", len(high.Alerts))
	}
	both, _ := ListAlerts(ctx, st.DB, AlertFilter{Status: AlertOpen, Severity: "high", Limit: 1})
	if len(both.Alerts) != 1 || both.Alerts[0].Rule != "test" {
		t.Errorf("open high, limit 1 = %+v", both.Alerts)
	}
}

func TestGetAndReviewAlert(t *testing.T) {
	st, _ := seedAlerts(t)
	ctx := context.Background()
	l, _ := ListAlerts(ctx, st.DB, AlertFilter{Status: AlertOpen, Severity: "medium"})
	id := l.Alerts[0].ID

	d, err := GetAlert(ctx, st.DB, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Alert.Rule != "pipe-to-shell" || d.RawRecord != `{"type":"assistant","note":"the agent's own record"}` || d.HistoryBegins.IsZero() {
		t.Errorf("detail = %+v", d)
	}
	// an alert whose action has no stored record, and one with no action at all
	l2, _ := ListAlerts(ctx, st.DB, AlertFilter{Severity: "high"})
	for _, a := range l2.Alerts {
		d, err := GetAlert(ctx, st.DB, a.ID)
		if err != nil || d.RawRecord != "" {
			t.Errorf("alert %d: %v, record %q", a.ID, err, d.RawRecord)
		}
	}
	if _, err := GetAlert(ctx, st.DB, 99999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown alert = %v", err)
	}

	first := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	if err := ReviewAlert(ctx, st.DB, id, true, first); err != nil {
		t.Fatal(err)
	}
	d, _ = GetAlert(ctx, st.DB, id)
	if d.Alert.Status != AlertReviewed || !d.Alert.ReviewedAt.Equal(first) {
		t.Errorf("reviewed = %+v", d.Alert)
	}
	// reviewing again keeps the first time
	if err := ReviewAlert(ctx, st.DB, id, true, first.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	d, _ = GetAlert(ctx, st.DB, id)
	if !d.Alert.ReviewedAt.Equal(first) {
		t.Errorf("review time changed to %v", d.Alert.ReviewedAt)
	}
	// and it can be reopened
	if err := ReviewAlert(ctx, st.DB, id, false, first); err != nil {
		t.Fatal(err)
	}
	d, _ = GetAlert(ctx, st.DB, id)
	if d.Alert.Status != AlertOpen || !d.Alert.ReviewedAt.IsZero() {
		t.Errorf("reopened = %+v", d.Alert)
	}
	if err := ReviewAlert(ctx, st.DB, 99999, true, first); !errors.Is(err, ErrNotFound) {
		t.Errorf("review unknown = %v", err)
	}
}

func TestAlertQueriesFailOnAClosedDatabase(t *testing.T) {
	st, _ := seedAlerts(t)
	st.DB.Close()
	ctx := context.Background()
	if _, err := ListAlerts(ctx, st.DB, AlertFilter{}); err == nil {
		t.Error("ListAlerts on a closed database")
	}
	if _, err := GetAlert(ctx, st.DB, 1); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("GetAlert on a closed database = %v", err)
	}
	if err := ReviewAlert(ctx, st.DB, 1, true, time.Now()); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("ReviewAlert on a closed database = %v", err)
	}
}

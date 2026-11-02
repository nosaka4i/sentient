package health

import (
	"strings"
	"testing"
	"time"
)

func TestWarnings(t *testing.T) {
	r := &Report{}
	if w := r.Warnings(); len(w) != 0 {
		t.Errorf("empty report warnings = %+v", w)
	}

	r = &Report{
		Integrity: Integrity{CheckedAt: time.Now(), OK: false, BadRecordID: 7},
		Sudo:      Sudo{Checked: true, Passwordless: []string{"ubuntu", "dev"}},
		Ingest:    []IngestAgent{{Agent: "claude", Unknown: 1}},
		Storage:   Storage{FreeBytes: 5, SizeBytes: 100},
	}
	w := r.Warnings()
	if len(w) != 5 {
		t.Fatalf("warnings = %d: %+v", len(w), w)
	}
	if !w[0].Critical || w[0].What != "Hash chain broken" {
		t.Errorf("first warning = %+v", w[0])
	}
	if w[1].Account != "ubuntu" || w[2].Account != "dev" || w[1].FixHref == "" {
		t.Errorf("sudo warnings = %+v %+v", w[1], w[2])
	}
	if w[3].What != "1 agent log record of unknown type" {
		t.Errorf("unknown warning = %q", w[3].What)
	}
	if w[4].What != "Disk almost full" {
		t.Errorf("disk warning = %q", w[4].What)
	}

	// A chain that hasn't been checked yet isn't broken.
	r = &Report{Integrity: Integrity{OK: false}, Ingest: []IngestAgent{{Unknown: 2}, {Unknown: 3}}}
	w = r.Warnings()
	if len(w) != 1 || w[0].What != "5 agent log records of unknown type" {
		t.Errorf("warnings = %+v", w)
	}
}

func TestCaptureWarnings(t *testing.T) {
	policyAt := func(level string) Capture {
		return Capture{Enabled: true, Level: "essential", PolicyKnown: true, PolicyLevel: level}
	}
	// agreeing, unknown, or capture off: nothing to say
	for name, c := range map[string]Capture{
		"agree":       policyAt("essential"),
		"unknown":     {Enabled: true, Level: "essential"},
		"capture off": {Level: "essential", PolicyKnown: true, PolicyLevel: "complete"},
	} {
		if w := (&Report{Capture: c}).Warnings(); len(w) != 0 {
			t.Errorf("%s: warnings = %+v", name, w)
		}
	}
	w := (&Report{Capture: policyAt("complete")}).Warnings()
	if len(w) != 1 || w[0].Critical || w[0].What != "The Tetragon policy records at complete, sentientd is set to essential" ||
		!strings.Contains(w[0].Why, "sen policy --verbosity essential") {
		t.Errorf("mismatch warning = %+v", w)
	}
}

func TestDiskWarningsFollowTheUsedShare(t *testing.T) {
	at := func(usedPct int64) []Warning {
		return (&Report{Storage: Storage{SizeBytes: 100_000_000_000, FreeBytes: (100 - usedPct) * 1_000_000_000, DBBytes: 700_000_000}}).Warnings()
	}
	if w := at(69); len(w) != 0 {
		t.Errorf("69%% used: %+v", w)
	}
	w := at(70)
	if len(w) != 1 || w[0].Critical || w[0].What != "Disk filling up" || !strings.Contains(w[0].Why, "700 MB") || !strings.Contains(w[0].Why, "70%") {
		t.Errorf("70%% used: %+v", w)
	}
	if w := at(89); len(w) != 1 || w[0].Critical {
		t.Errorf("89%% used: %+v", w)
	}
	w = at(90)
	if len(w) != 1 || !w[0].Critical || w[0].What != "Disk almost full" || strings.Contains(w[0].Why, "stops") {
		t.Errorf("90%% used: %+v", w) // recording does not stop by itself yet, so the text must not say it does
	}
	if w := (&Report{}).Warnings(); len(w) != 0 {
		t.Errorf("size unknown: %+v", w)
	}
}

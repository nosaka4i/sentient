package health

import (
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

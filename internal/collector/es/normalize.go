package es

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nosaka4i/sentient/internal/collector"
	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/store"
)

// Open flags as the kernel's file flags (the ES open event's fflag): read is
// bit 0 and write is bit 1; the higher bits are close-on-exec and similar.
const (
	flagRead  = 0x1
	flagWrite = 0x2
)

// procKey identifies a process image: a pid is reused, and its pid version
// changes on reuse and on exec.
type procKey struct{ pid, ver int64 }

// proc is what the normalizer knows about a process of the agent tree.
type proc struct {
	execID string
	alias  bool // a fork child that has not exec'd yet: it shares its parent's image and exec_id
}

// Normalizer turns the extension's messages into sentient's OS-neutral
// records and hands them to a Sink. The extension already keeps only the agent
// tree, so every process message here belongs to it; the normalizer builds the
// exec_id, links parents, and maps operations to event types. It is not safe
// for concurrent use: the collector calls it from one goroutine.
type Normalizer struct {
	sink  collector.Sink
	procs map[procKey]proc
	byPID map[int64]procKey // the current key of each pid: an exec changes the version, so the old key must go
}

// NewNormalizer returns a Normalizer that reports to sink.
func NewNormalizer(sink collector.Sink) *Normalizer {
	return &Normalizer{sink: sink, procs: map[procKey]proc{}, byPID: map[int64]procKey{}}
}

// track records p under key, dropping the pid's previous key (the image that
// an exec replaced, or a pid that was reused).
func (n *Normalizer) track(key procKey, p proc) {
	if old, ok := n.byPID[key.pid]; ok && old != key {
		delete(n.procs, old)
	}
	n.procs[key] = p
	n.byPID[key.pid] = key
}

func (n *Normalizer) forget(key procKey) {
	delete(n.procs, key)
	if n.byPID[key.pid] == key {
		delete(n.byPID, key.pid)
	}
}

// Known is how many processes of the tree are currently tracked.
func (n *Normalizer) Known() int { return len(n.procs) }

// ExecID builds a process's exec_id: es:<pid>:<pidversion>:<start>.
func ExecID(pid, ver, start int64) string {
	return fmt.Sprintf("es:%d:%d:%d", pid, ver, start)
}

func at(m Message) time.Time { return time.Unix(m.TSSec, m.TSNsec).UTC() }

// Handle processes one message. Kinds that are not about a process (hello,
// ping, stats) are the collector's business and are ignored here, as are
// messages about a process the normalizer has not seen start.
func (n *Normalizer) Handle(ctx context.Context, m Message) error {
	switch m.T {
	case KindExec:
		return n.exec(ctx, m)
	case KindFork:
		n.fork(m)
	case KindExit:
		return n.exit(ctx, m)
	case KindFile:
		return n.file(ctx, m)
	}
	return nil
}

func (n *Normalizer) exec(ctx context.Context, m Message) error {
	key := procKey{m.PID, m.PIDVer}
	id := ExecID(m.PID, m.PIDVer, m.Start)
	parent := n.procs[procKey{m.PPID, m.PPIDVer}].execID
	// An exec by a forked child keeps the pid; the old image ends here without
	// an exit of its own.
	n.track(key, proc{execID: id})
	started := at(m)
	if m.Start != 0 {
		started = time.Unix(m.Start, 0).UTC()
	}
	if err := n.sink.Process(ctx, store.Process{
		ExecID:       id,
		Collector:    Name,
		PID:          m.PID,
		ParentExecID: parent,
		Binary:       m.Exe,
		Argv:         mustJSON(orEmpty(m.Args)),
		Cwd:          m.Cwd,
		UID:          m.UID,
		StartedAt:    dbtime.Format(started),
	}); err != nil {
		return err
	}
	return n.sink.Event(ctx, store.KernelEvent{
		ExecID: id, TS: dbtime.Format(at(m)), Type: "exec", Path: m.Exe,
		Payload: mustJSON(map[string]any{"argv": orEmpty(m.Args), "cwd": m.Cwd, "pid": m.PID}),
	})
}

// fork registers the child as sharing its parent's image until it execs, so
// its file events belong to the parent's exec_id (as in Tetragon's model).
func (n *Normalizer) fork(m Message) {
	parent, ok := n.procs[procKey{m.PPID, m.PPIDVer}]
	if !ok {
		return
	}
	n.track(procKey{m.PID, m.PIDVer}, proc{execID: parent.execID, alias: true})
}

func (n *Normalizer) exit(ctx context.Context, m Message) error {
	key := procKey{m.PID, m.PIDVer}
	p, ok := n.procs[key]
	if !ok {
		return nil
	}
	n.forget(key)
	if p.alias {
		return nil // the child never had an image of its own; the parent's exec is still running
	}
	return n.sink.Exit(ctx, p.execID, at(m), m.Status)
}

// adopt registers a process whose start the normalizer did not see: the
// daemon restarted while the extension kept running, so the agent's processes
// are alive and report file events without a new exec. Every message of the
// extension is about a process of the agent tree, so the process is recorded
// from the fields the message carries. Without an executable there is nothing
// to record and the message is dropped.
func (n *Normalizer) adopt(ctx context.Context, m Message) (proc, bool, error) {
	if m.Exe == "" {
		return proc{}, false, nil
	}
	id := ExecID(m.PID, m.PIDVer, m.Start)
	p := proc{execID: id}
	n.track(procKey{m.PID, m.PIDVer}, p)
	started := at(m)
	if m.Start != 0 {
		started = time.Unix(m.Start, 0).UTC()
	}
	err := n.sink.Process(ctx, store.Process{
		ExecID:       id,
		Collector:    Name,
		PID:          m.PID,
		ParentExecID: n.procs[procKey{m.PPID, m.PPIDVer}].execID,
		Binary:       m.Exe,
		Argv:         "[]",
		UID:          m.UID,
		StartedAt:    dbtime.Format(started),
	})
	return p, true, err
}

// Reset forgets every tracked process. The collector calls it when the
// extension reconnects: after the extension restarted, its tree is new.
func (n *Normalizer) Reset() {
	n.procs = map[procKey]proc{}
	n.byPID = map[int64]procKey{}
}

func (n *Normalizer) file(ctx context.Context, m Message) error {
	p, ok := n.procs[procKey{m.PID, m.PIDVer}]
	if !ok {
		var err error
		if p, ok, err = n.adopt(ctx, m); err != nil || !ok {
			return err
		}
	}
	e := store.KernelEvent{ExecID: p.execID, TS: dbtime.Format(at(m)), Path: m.Path, Payload: "{}"}
	switch m.Op {
	case OpCreate:
		e.Type = "file_create"
	case OpWrite:
		e.Type = "file_write"
	case OpUnlink:
		e.Type = "file_unlink"
	case OpRename:
		e.Type, e.Path2 = "file_rename", m.Path2
	case OpOpen:
		e.Type = "file_open"
		e.Payload = mustJSON(map[string]any{"flags": m.Flags, "perm": perm(m.Flags)})
	default:
		return nil
	}
	if e.Path == "" || (e.Type == "file_rename" && e.Path2 == "") {
		return nil
	}
	return n.sink.Event(ctx, e)
}

// perm reads an open's flags: "r", "w" or "rw".
func perm(flags int64) string {
	switch {
	case flags&flagRead != 0 && flags&flagWrite != 0:
		return "rw"
	case flags&flagWrite != 0:
		return "w"
	default:
		return "r"
	}
}

func orEmpty(a []string) []string {
	if a == nil {
		return []string{}
	}
	return a
}

// mustJSON encodes v as compact JSON without escaping &, < and >, so a stored
// command line (`a && b < c`) stays readable as evidence.
func mustJSON(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "{}"
	}
	return strings.TrimSuffix(b.String(), "\n")
}

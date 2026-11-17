package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nosaka4i/sentient/internal/collector/es"
)

// protoEvent is one line of the prototype extension's capture
// (apps/mac/prototype): the older, file format. Only the fields the replay
// needs are read.
type protoEvent struct {
	Type     string   `json:"type"`
	TSSec    int64    `json:"ts_sec"`
	TSNsec   int64    `json:"ts_nsec"`
	PID      int64    `json:"pid"`
	PPID     int64    `json:"ppid"`
	ChildPID int64    `json:"child_pid"` // fork: the new process (pid is the parent)
	Exe      string   `json:"exe"`       // the process's executable at the time
	Target   string   `json:"target"`    // exec: the new program
	Args     []string `json:"args"`
	Path     string   `json:"path"`
	From     string   `json:"from"` // rename
	To       string   `json:"to"`
	Flags    int64    `json:"flags"`
	Status   int      `json:"status"`
}

// procState is what the converter invents for a process, because the
// prototype recorded neither a pid version nor a start time: the version
// starts at 1 and goes up on every exec (as the real extension's does), and
// the start time is the first time the process shows up.
type procState struct{ ver, start int64 }

// Converter turns prototype capture lines into the production schema
// (internal/collector/es/message.go). It is a stand-in for the extension while
// that is not built: the data is real, the process identity is approximated.
// It drops what the production extension would not send: closes, and writes to
// terminals and /dev/null.
type Converter struct {
	UID int64 // the user id given to every process; the prototype did not record it

	seq     uint64
	started bool
	procs   map[int64]*procState
}

func (c *Converter) proc(pid, ts int64) *procState {
	if c.procs == nil {
		c.procs = map[int64]*procState{}
	}
	p := c.procs[pid]
	if p == nil {
		p = &procState{ver: 1, start: ts}
		c.procs[pid] = p
	}
	return p
}

func (c *Converter) next(m es.Message) es.Message {
	c.seq++
	m.V, m.Seq = es.SchemaVersion, c.seq
	return m
}

// Ping returns a ping message with the next sequence number.
func (c *Converter) Ping(sec int64) es.Message {
	return c.next(es.Message{T: es.KindPing, TSSec: sec})
}

// Convert turns one capture line into zero or more messages. The first call
// also returns the hello that opens a connection. An unreadable line is an
// error; a line that is not sent (a close, a terminal write) is not.
func (c *Converter) Convert(line []byte) ([]es.Message, error) {
	var e protoEvent
	if err := json.Unmarshal(line, &e); err != nil {
		return nil, fmt.Errorf("not a capture line: %w", err)
	}
	if e.Type == "" || e.PID == 0 {
		return nil, fmt.Errorf("capture line has no type or pid")
	}
	var out []es.Message
	if !c.started {
		c.started = true
		out = append(out, c.next(es.Message{T: es.KindHello, TSSec: e.TSSec, Ext: "replay", Build: "0",
			MacOS: "replayed from a prototype capture", ESClient: "ok"}))
	}
	msg, ok := c.event(e)
	if ok {
		out = append(out, c.next(msg))
	}
	return out, nil
}

// base fills what every process message carries.
func (c *Converter) base(e protoEvent, p *procState) es.Message {
	return es.Message{
		TSSec: e.TSSec, TSNsec: e.TSNsec,
		PID: e.PID, PIDVer: p.ver, PPID: e.PPID, PPIDVer: c.proc(e.PPID, e.TSSec).ver,
		Start: p.start, UID: c.UID, Exe: e.Exe,
	}
}

func (c *Converter) event(e protoEvent) (es.Message, bool) {
	p := c.proc(e.PID, e.TSSec)
	switch e.Type {
	case "fork":
		child := &procState{ver: 1, start: e.TSSec}
		c.procs[e.ChildPID] = child
		return es.Message{T: es.KindFork, TSSec: e.TSSec, TSNsec: e.TSNsec,
			PID: e.ChildPID, PIDVer: 1, PPID: e.PID, PPIDVer: p.ver, Start: e.TSSec, UID: c.UID}, true
	case "exec":
		p.ver++ // an exec gives the process a new version
		m := c.base(e, p)
		m.T, m.Exe, m.Args = es.KindExec, e.Target, e.Args
		return m, true
	case "exit":
		m := c.base(e, p)
		m.T, m.Status = es.KindExit, e.Status
		delete(c.procs, e.PID)
		return m, true
	case "create", "write", "unlink", "open", "rename":
		if e.Type == "write" && (strings.HasPrefix(e.Path, "/dev/tty") || e.Path == "/dev/null") {
			return es.Message{}, false // the real extension mutes terminal writes in the kernel
		}
		m := c.base(e, p)
		m.T, m.Op, m.Path = es.KindFile, e.Type, e.Path
		switch e.Type {
		case "open":
			m.Flags = e.Flags
		case "rename":
			m.Path, m.Path2 = e.From, e.To
		}
		return m, true
	}
	return es.Message{}, false // close, and anything the production extension does not send
}

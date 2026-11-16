// Package es is the Mac collector: it reads what the SentientES extension
// (apps/mac, an Endpoint Security client) sends over a Unix socket and reports
// it to a collector.Sink, the same pipeline Tetragon feeds on Linux. The
// package imports no Apple code, so it builds and tests everywhere. See
// docs/mac-collector-design.md.
package es

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Name is the collector's identifier as stored in processes.collector.
const Name = "endpointsecurity"

// SchemaVersion is the wire schema this package reads (the "v" of a message).
const SchemaVersion = 1

// Message kinds (the "t" of a message).
const (
	KindHello = "hello" // the extension, on connect: its version and whether the ES client started
	KindExec  = "exec"  // a process in the agent tree ran a new program
	KindFork  = "fork"  // a process in the agent tree forked
	KindExit  = "exit"  // a process in the agent tree ended
	KindFile  = "file"  // a file operation by a process in the agent tree
	KindPing  = "ping"  // liveness, with the latest ES sequence numbers
	KindStats = "stats" // counts of events the extension dropped
)

// File operations (the "op" of a file message), named as Endpoint Security
// names them.
const (
	OpCreate = "create"
	OpWrite  = "write"
	OpUnlink = "unlink"
	OpRename = "rename"
	OpOpen   = "open"
)

// Message is one JSON line from the extension. It is a flat record with every
// field optional, because the kinds share most of them; which fields a kind
// carries is in docs/mac-collector-design.md. The extension sends raw values
// and does no normalization: paths verbatim, open flags as the kernel gave
// them, process identity as separate numbers.
type Message struct {
	V int    `json:"v"`
	T string `json:"t"`

	// Seq numbers the extension's own messages, so a jump shows a drop.
	Seq    uint64 `json:"seq,omitempty"`
	TSSec  int64  `json:"ts_sec,omitempty"`
	TSNsec int64  `json:"ts_nsec,omitempty"`

	// The process the message is about (exec, fork, exit, file) and its parent.
	PID     int64  `json:"pid,omitempty"`
	PIDVer  int64  `json:"pidver,omitempty"` // changes when a pid is reused and on exec
	PPID    int64  `json:"ppid,omitempty"`
	PPIDVer int64  `json:"ppidver,omitempty"`
	Start   int64  `json:"start,omitempty"` // process start, unix seconds
	UID     int64  `json:"uid,omitempty"`
	Exe     string `json:"exe,omitempty"` // the process's executable; for exec, the new program

	// exec
	Args []string `json:"args,omitempty"`
	Cwd  string   `json:"cwd,omitempty"`

	// exit
	Status int `json:"status,omitempty"`

	// file
	Op    string `json:"op,omitempty"`
	Path  string `json:"path,omitempty"`
	Path2 string `json:"path2,omitempty"` // a rename's destination
	Flags int64  `json:"flags,omitempty"` // an open's flags, raw

	// hello
	Ext       string `json:"ext,omitempty"`        // extension version
	Build     string `json:"build,omitempty"`      // extension build number
	MacOS     string `json:"macos,omitempty"`      // macOS version
	ESVersion int    `json:"es_version,omitempty"` // Endpoint Security message version
	BootID    string `json:"boot_id,omitempty"`
	ESClient  string `json:"es_client,omitempty"` // "ok", or why es_new_client failed ("not_permitted", ...)

	// ping: the latest sequence numbers Endpoint Security itself reported
	ESSeq       uint64 `json:"es_seq,omitempty"`
	ESGlobalSeq uint64 `json:"es_global_seq,omitempty"`

	// stats: events the extension dropped, by reason
	Dropped map[string]int64 `json:"dropped,omitempty"`
}

// ErrVersion is returned for a message whose schema version this package does
// not read.
var ErrVersion = errors.New("unsupported schema version")

// Decode parses one line. Unknown fields are ignored, so a newer extension can
// add fields without breaking an older daemon; a message with a newer schema
// version, no version or no kind is an error.
func Decode(line []byte) (Message, error) {
	var m Message
	if err := json.Unmarshal(line, &m); err != nil {
		return Message{}, fmt.Errorf("decode: %w", err)
	}
	if m.V == 0 || m.T == "" {
		return Message{}, errors.New("decode: no version or kind")
	}
	if m.V > SchemaVersion {
		return m, fmt.Errorf("%w: %d", ErrVersion, m.V)
	}
	return m, nil
}

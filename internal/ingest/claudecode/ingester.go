// Package claudecode ingests Claude Code transcripts: it tails
// <home>/.claude/projects/*/<session>.jsonl and
// <session>/subagents/*.jsonl, copies every raw record into the store
// verbatim, and extracts sessions, prompts and tool calls as normalized
// actions (evidence "log").
package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/store"
)

// Agent is the agent name stored on sessions.
const Agent = "claude"

// batchSize is how many records go into one transaction.
const batchSize = 200

// Config configures an Ingester.
type Config struct {
	// Homes are the home directories whose ~/.claude/projects is tailed.
	Homes []string
	// PollInterval is how often files are checked for new records.
	PollInterval time.Duration
	Logger       *slog.Logger
}

// Ingester tails transcripts into the store. Only one should run per store.
type Ingester struct {
	st  *store.Store
	cfg Config
	log *slog.Logger

	mu        sync.Mutex
	repoRoots map[string]string // cwd -> repo root ("" = not in a repo)
	unknown   map[string]int    // record type -> count since start
}

// New returns an Ingester writing to st.
func New(st *store.Store, cfg Config) *Ingester {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Ingester{
		st:        st,
		cfg:       cfg,
		log:       log.With("component", "ingest.claudecode"),
		repoRoots: map[string]string{},
		unknown:   map[string]int{},
	}
}

// Run scans until ctx is cancelled.
func (in *Ingester) Run(ctx context.Context) error {
	t := time.NewTicker(in.cfg.PollInterval)
	defer t.Stop()
	for {
		if err := in.ScanOnce(ctx); err != nil && ctx.Err() == nil {
			in.log.Error("scan failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// UnknownCounts returns unknown record types seen since start.
func (in *Ingester) UnknownCounts() map[string]int {
	in.mu.Lock()
	defer in.mu.Unlock()
	out := make(map[string]int, len(in.unknown))
	for k, v := range in.unknown {
		out[k] = v
	}
	return out
}

// Files lists the transcript files under the configured homes: main
// session files first, then subagent files, each sorted, so a session
// exists before its subagents' records arrive.
func (in *Ingester) Files() []string {
	var mains, subs []string
	for _, home := range in.cfg.Homes {
		root := filepath.Join(home, ".claude", "projects")
		m, _ := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
		s, _ := filepath.Glob(filepath.Join(root, "*", "*", "subagents", "*.jsonl"))
		mains = append(mains, m...)
		subs = append(subs, s...)
	}
	sort.Strings(mains)
	sort.Strings(subs)
	return append(mains, subs...)
}

// ScanOnce ingests whatever is new in every transcript file.
func (in *Ingester) ScanOnce(ctx context.Context) error {
	var errs []error
	for _, f := range in.Files() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := in.IngestFile(ctx, f); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f, err))
		}
	}
	return errors.Join(errs...)
}

// sessionFromPath derives the session id from a transcript path:
// .../<project>/<session>.jsonl or .../<project>/<session>/subagents/x.jsonl.
func sessionFromPath(path string) string {
	dir := filepath.Dir(path)
	if filepath.Base(dir) == "subagents" {
		return filepath.Base(filepath.Dir(dir))
	}
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

// IngestFile ingests complete lines appended to path since the last call,
// resuming from the offset stored with the records.
func (in *Ingester) IngestFile(ctx context.Context, path string) error {
	off, err := in.st.Offset(ctx, path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Size() < off {
		in.log.Warn("transcript shrank; re-reading from the start", "file", path, "size", fi.Size(), "offset", off)
		off = 0
	}
	if fi.Size() == off {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReaderSize(f, 1<<20)
	fallbackSession := sessionFromPath(path)

	tx, err := in.st.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { tx.Rollback() }()
	n := 0
	for {
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			break // a partial last line is read on a later scan, once complete
		}
		if err != nil {
			return err
		}
		off += int64(len(line))
		if err := in.ingestLine(ctx, tx, path, fallbackSession, bytes.TrimSpace(line)); err != nil {
			return err
		}
		n++
		if n%batchSize == 0 {
			if err := tx.SetOffset(ctx, path, off); err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			if tx, err = in.st.Begin(ctx); err != nil {
				return err
			}
		}
	}
	if err := tx.SetOffset(ctx, path, off); err != nil {
		return err
	}
	return tx.Commit()
}

func (in *Ingester) noteUnknown(typ, path string) {
	in.mu.Lock()
	in.unknown[typ]++
	c := in.unknown[typ]
	in.mu.Unlock()
	// Loud on first sight of each type, then every 1000th so logs stay usable.
	if c == 1 || c%1000 == 0 {
		in.log.Warn("UNKNOWN Claude Code record type: stored raw, not interpreted",
			"type", typ, "file", path, "count", c)
	}
}

// normTime converts a record timestamp to the stored form ("" if absent
// or unparsable).
func normTime(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return ""
	}
	return dbtime.Format(t)
}

func (in *Ingester) ingestLine(ctx context.Context, tx *store.Tx, path, fallbackSession string, line []byte) error {
	if len(line) == 0 {
		return nil
	}
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		in.noteUnknown("<unparsable>", path)
		_, err := tx.AppendRaw(ctx, store.RawRecord{SourceFile: path, Type: "", Unknown: true, Record: string(line)})
		return err
	}
	unknown := !knownTypes[rec.Type]
	if unknown {
		name := rec.Type
		if name == "" {
			name = "<missing type>"
		}
		in.noteUnknown(name, path)
	}

	ts := normTime(rec.Timestamp)
	sid := rec.SessionID
	if sid == "" {
		sid = fallbackSession
	}
	sessionID, err := tx.UpsertSession(ctx, store.Session{
		Agent:          Agent,
		AgentSessionID: sid,
		Cwd:            rec.Cwd,
		RepoRoot:       in.repoRoot(rec.Cwd),
		TS:             ts,
		AgentVersion:   rec.Version,
	})
	if err != nil {
		return err
	}
	rawID, err := tx.AppendRaw(ctx, store.RawRecord{
		SessionID: sessionID, TS: ts, SourceFile: path, Type: rec.Type, Unknown: unknown, Record: string(line),
	})
	if err != nil {
		return err
	}
	if unknown || ts == "" {
		return nil
	}

	switch rec.Type {
	case "user":
		return in.ingestUser(ctx, tx, &rec, sessionID, ts)
	case "assistant":
		return in.ingestAssistant(ctx, tx, &rec, sessionID, ts, rawID)
	}
	return nil
}

func (in *Ingester) ingestUser(ctx context.Context, tx *store.Tx, rec *record, sessionID int64, ts string) error {
	if text, ok := promptText(rec); ok {
		_, err := tx.AddPrompt(ctx, sessionID, text, ts)
		return err
	}
	var results []block
	for _, b := range rec.Message.contentBlocks() {
		if b.Type == "tool_result" && b.ToolUseID != "" {
			results = append(results, b)
		}
	}
	for _, b := range results {
		tr := store.ToolResult{ToolUseID: b.ToolUseID, Failed: b.IsError}
		if len(results) == 1 { // toolUseResult describes the record's single result
			tr.Action, tr.LinesAdded, tr.LinesRemoved = resultDetails(rec.ToolUseResult)
		}
		if _, err := tx.ApplyToolResult(ctx, tr); err != nil {
			return err
		}
	}
	return nil
}

func (in *Ingester) ingestAssistant(ctx context.Context, tx *store.Tx, rec *record, sessionID int64, ts string, rawID int64) error {
	var promptID int64
	for _, b := range rec.Message.contentBlocks() {
		if b.Type != "tool_use" {
			continue
		}
		acts := mapTool(b.Name, b.Input)
		if len(acts) == 0 {
			continue
		}
		if promptID == 0 {
			var err error
			if promptID, err = tx.PromptAt(ctx, sessionID, ts); err != nil {
				return err
			}
		}
		for part, m := range acts {
			if err := tx.AddAction(ctx, store.Action{
				SessionID:   sessionID,
				PromptID:    promptID,
				TS:          ts,
				Resource:    m.Resource,
				Action:      m.Action,
				Path:        m.Path,
				Summary:     m.Summary,
				Evidence:    "log",
				RawRecordID: rawID,
				ToolUseID:   b.ID,
				Part:        part,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// repoRoot resolves the repo root from a path by walking up to the nearest
// .git (design: resolved by us, not taken from the log). Cached per cwd.
func (in *Ingester) repoRoot(cwd string) string {
	if cwd == "" {
		return ""
	}
	in.mu.Lock()
	r, ok := in.repoRoots[cwd]
	in.mu.Unlock()
	if ok {
		return r
	}
	r = FindRepoRoot(cwd)
	in.mu.Lock()
	in.repoRoots[cwd] = r
	in.mu.Unlock()
	return r
}

// FindRepoRoot returns the nearest ancestor of dir (inclusive) containing
// .git, or "" if none.
func FindRepoRoot(dir string) string {
	dir = filepath.Clean(dir)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

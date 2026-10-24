package tetragon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/cilium/tetragon/api/v1/tetragon"

	"github.com/nosaka4i/sentient/internal/collector"
	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/store"
)

// Name is the collector's identifier as stored.
const Name = "tetragon"

// Normalizer turns Tetragon events into sentient's OS-neutral records and
// hands them to a Sink. It keeps only the agent's process tree: Tetragon
// reports every exec on the box.
type Normalizer struct {
	sink     collector.Sink
	prefixes []string
	kept     map[string]bool // exec_id -> in the agent tree
}

// NewNormalizer returns a Normalizer that keeps processes whose binary matches
// one of agentPrefixes (see policy.AgentPrefixes: an entry ending in "/"
// matches everything under it, any other entry matches that file exactly) and
// everything they start.
func NewNormalizer(sink collector.Sink, agentPrefixes []string) *Normalizer {
	return &Normalizer{sink: sink, prefixes: agentPrefixes, kept: map[string]bool{}}
}

func (n *Normalizer) isAgent(binary string) bool {
	for _, p := range n.prefixes {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(binary, p) {
				return true
			}
		} else if binary == p {
			return true
		}
	}
	return false
}

// Handle processes one event from the stream. Events that are not part of the
// agent tree, or of a kind we don't record, are dropped without error.
func (n *Normalizer) Handle(ctx context.Context, r *tetragon.GetEventsResponse) error {
	at := r.GetTime().AsTime()
	switch ev := r.GetEvent().(type) {
	case *tetragon.GetEventsResponse_ProcessExec:
		p := ev.ProcessExec.GetProcess()
		ok, err := n.see(ctx, p)
		if err != nil || !ok {
			return err
		}
		payload := mustJSON(map[string]any{"argv": splitArgs(p.GetArguments()), "cwd": p.GetCwd(), "pid": p.GetPid().GetValue()})
		return n.sink.Event(ctx, store.KernelEvent{ExecID: p.GetExecId(), TS: dbtime.Format(at), Type: "exec", Path: p.GetBinary(), Payload: payload})
	case *tetragon.GetEventsResponse_ProcessExit:
		x := ev.ProcessExit
		id := x.GetProcess().GetExecId()
		if !n.kept[id] {
			return nil
		}
		delete(n.kept, id)
		if x.GetTime() != nil {
			at = x.GetTime().AsTime()
		}
		return n.sink.Exit(ctx, id, at, int(x.GetStatus()))
	case *tetragon.GetEventsResponse_ProcessKprobe:
		k := ev.ProcessKprobe
		typ, p1, p2, payload, ok := mapKprobe(k)
		if !ok {
			return nil
		}
		// a process that started before the collector shows up first in its
		// own events; register it here if it belongs to the tree
		if ok, err := n.see(ctx, k.GetProcess()); err != nil || !ok {
			return err
		}
		return n.sink.Event(ctx, store.KernelEvent{ExecID: k.GetProcess().GetExecId(), TS: dbtime.Format(at), Type: typ, Path: p1, Path2: p2, Payload: payload})
	}
	return nil
}

// see decides whether a process belongs to the agent tree and, the first
// time it does, reports it to the sink.
func (n *Normalizer) see(ctx context.Context, p *tetragon.Process) (bool, error) {
	id := p.GetExecId()
	if id == "" {
		return false, nil
	}
	if n.kept[id] {
		return true, nil
	}
	if !n.isAgent(p.GetBinary()) && !n.kept[p.GetParentExecId()] {
		return false, nil
	}
	n.kept[id] = true
	parent := ""
	if n.kept[p.GetParentExecId()] {
		parent = p.GetParentExecId()
	}
	started := time.Now()
	if p.GetStartTime() != nil {
		started = p.GetStartTime().AsTime()
	}
	err := n.sink.Process(ctx, store.Process{
		ExecID:       id,
		Collector:    Name,
		PID:          int64(p.GetPid().GetValue()),
		ParentExecID: parent,
		Binary:       p.GetBinary(),
		Argv:         mustJSON(splitArgs(p.GetArguments())),
		Cwd:          p.GetCwd(),
		UID:          int64(p.GetUid().GetValue()),
		StartedAt:    dbtime.Format(started),
	})
	return true, err
}

// mapKprobe maps a hook to an event type, its paths and a compact payload.
// ok is false for hooks we don't record yet.
func mapKprobe(k *tetragon.ProcessKprobe) (typ, p1, p2, payload string, ok bool) {
	args := k.GetArgs()
	fn := k.GetFunctionName()
	switch {
	case fn == "security_file_open":
		f := argPath(args, 0)
		return "file_open", cleanPath(f.path), "", mustJSON(map[string]any{"perm": f.perm}), f.path != ""
	case fn == "security_path_mknod":
		return dirEntry("file_create", args)
	case fn == "security_path_mkdir":
		return dirEntry("dir_create", args)
	case fn == "security_path_rmdir":
		return dirEntry("dir_delete", args)
	case fn == "security_path_unlink":
		return dirEntry("file_unlink", args)
	case fn == "security_path_rename":
		from := entryPath(argPath(args, 0).path, argPath(args, 1).path)
		to := entryPath(argPath(args, 2).path, argPath(args, 3).path)
		return "file_rename", from, to, "{}", from != "" && to != ""
	case fn == "security_path_chmod":
		f := argPath(args, 0)
		mode := ""
		if len(args) > 1 {
			mode = fmt.Sprintf("%04o", args[1].GetUintArg())
		}
		return "file_setmode", cleanPath(f.path), "", mustJSON(map[string]any{"mode": mode}), f.path != ""
	case fn == "tcp_connect":
		if len(args) == 0 || args[0].GetSockArg() == nil {
			return "", "", "", "", false
		}
		s := args[0].GetSockArg()
		dest := net.JoinHostPort(s.GetDaddr(), strconv.Itoa(int(s.GetDport())))
		return "net_connect", dest, "", mustJSON(map[string]any{
			"family": s.GetFamily(), "saddr": s.GetSaddr(), "sport": s.GetSport(),
			"daddr": s.GetDaddr(), "dport": s.GetDport(), "protocol": s.GetProtocol(),
		}), true
	case strings.HasSuffix(fn, "sys_kill") || strings.HasSuffix(fn, "sys_tgkill"):
		if len(args) < 2 {
			return "", "", "", "", false
		}
		// kill(pid, sig); tgkill(tgid, tid, sig) has the signal last
		sig := args[len(args)-1].GetIntArg()
		return "kill", "", "", mustJSON(map[string]any{"target": args[0].GetIntArg(), "signal": sig}), true
	}
	return "", "", "", "", false
}

type pathArg struct{ path, perm string }

// argPath reads a path (or file) argument; zero when it isn't one.
func argPath(args []*tetragon.KprobeArgument, i int) pathArg {
	if i >= len(args) {
		return pathArg{}
	}
	if p := args[i].GetPathArg(); p != nil {
		return pathArg{p.GetPath(), p.GetPermission()}
	}
	if f := args[i].GetFileArg(); f != nil {
		return pathArg{f.GetPath(), f.GetPermission()}
	}
	return pathArg{}
}

// dirEntry maps hooks that report (parent directory, entry): the entry's full
// path is the event's path.
func dirEntry(typ string, args []*tetragon.KprobeArgument) (string, string, string, string, bool) {
	p := entryPath(argPath(args, 0).path, argPath(args, 1).path)
	return typ, p, "", "{}", p != ""
}

// entryPath is the full path of an entry. Some hooks report it relative to its
// mount (a tmpfs under /run, say) instead of in full; then join the directory
// and the entry's name.
func entryPath(dir, entry string) string {
	if entry == "" {
		return ""
	}
	if dir != "" && !strings.HasPrefix(entry, dir) {
		entry = path.Join(dir, path.Base(entry))
	}
	return cleanPath(entry)
}

// cleanPath drops the " (deleted)" suffix the kernel adds to the path of a
// file that has just been unlinked.
func cleanPath(p string) string { return strings.TrimSuffix(p, " (deleted)") }

// splitArgs splits Tetragon's single arguments string into an argv. Arguments
// containing spaces arrive in double quotes with backslash escapes.
func splitArgs(s string) []string {
	out := []string{}
	var cur strings.Builder
	inQuote, started := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && inQuote && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		case c == '"':
			inQuote, started = !inQuote, true
		case c == ' ' && !inQuote:
			if started {
				out = append(out, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	if started {
		out = append(out, cur.String())
	}
	return out
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

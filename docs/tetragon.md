# Tetragon as sentient's Linux collector

**Status: reference doc.** Evaluation of 2026-10-17 against Tetragon v1.7.1
on this dev box (Ubuntu, kernel 7.0.0-1014-aws, x86_64, 2 vCPU, ~2 GB RAM).
Raw captures: `.claudetmp/tetragon/` (run1 = planted Claude Code session,
run2/run3 = signal, ptrace, sudo and long-argv checks).

## Verdict: fits, with gaps we fill ourselves

Tetragon is a good base for the Linux collector. In one planted `claude -p`
session it recorded **every** planted action, attributed to the claude
process tree, with full paths, argv, cwd, exit codes, destination ip:port,
bytes per connection, and even hostnames, with **0 lost events**. It ran
standalone from the release tarball (no Kubernetes, no install). The kernel
did the filtering to the claude tree, so nothing outside the tree reached
userspace except process exec/exit. Footprint was ~48 MB RSS and <1% CPU when
idle.

What we must build or decide ourselves (none of it blocks adoption):

1. **Starting the tree.** `matchBinaries` + `followChildren` only catches a
   `claude` that is exec'd **after** the policy loads. Tetragon must start
   before Claude Code (at boot). Already-running sessions need a fallback.
2. **Loss detection is counters, not sequence numbers.** Events carry no
   sequence number. We poll Prometheus counters and treat any increase, or
   Tetragon not running, as a `monitor gap`.
3. **Volume and event size.** About 220 events/s during an active session,
   77% of them `file_open` of shared libraries and `/proc`. Each event is
   about 10 KB of JSON when `--enable-ancestors` is on. We need path
   selectors and field filters, and we should read events over gRPC rather
   than from the JSON file.
4. **Normalization quirks.** Paths of newly created files end in ` (deleted)`.
   Edits land as tmp-file + rename. Exit status is omitted when 0.

## Requirement by requirement

| # | Requirement | Supported? | How | Evidence (run1 unless noted) |
|---|---|---|---|---|
| 1 | exec/exit, argv, cwd, pid/ppid, exit code, timestamps | **Yes** | base sensor: `process_exec`, `process_exit` (always on, system-wide) | z.sh, ls (child of z.sh), rm, curl, python3, lsof, head and Claude's own git/rg helpers all seen with argv and cwd. Exit `status` is present (git → 1) and omitted when 0. `exec_id`/`parent_exec_id` are unique across pid reuse. A 12,016-char argument was captured whole (run2). |
| 2a | open/read | **Yes** | kprobe `security_file_open` (type `file`, `FileType reg`) | claude opened `proj/x` 5×; python opened `s.py`; bash opened `z.sh`. Reads are logged per open, not per `read()`. |
| 2b | create | **Yes** | kprobe `security_path_mknod` (open(O_CREAT) goes through `may_o_create`) | python3 → `proj/settings.json`; claude → `x.tmp.155005.*`, `s.py.tmp.*`. The path has a ` (deleted)` suffix (the dentry is still negative), which we strip. |
| 2c | write/modify | **Yes** | kprobe `security_file_permission`, mask `MAY_WRITE`, `FileType reg` | One event per `write()`: claude → `x.tmp…`, python3 → `settings.json`. |
| 2d | Edit tool write by claude itself | **Yes** | mknod → write → chmod → **rename** `x.tmp.155005.* → x` | Claude Code writes atomically. sentient must treat a rename onto a path as a modification of that path. |
| 2e | unlink | **Yes** | kprobe `security_path_unlink` | `rm` (pid 155389) → `proj/junk.txt` |
| 2f | rename, chmod/chown, mkdir/rmdir, truncate | **Yes** | `security_path_rename/chmod/chown/mkdir/rmdir/truncate` | rename/chmod/mkdir/rmdir all fired (mostly Claude Code housekeeping under `~/.claude`). No chown or truncate happened in the session. |
| 3a | outbound connect ip:port | **Yes** | kprobes `tcp_connect`, `tcp_close` (type `sock`; from the official `tcp-connect.yaml`) | curl and python3 (urllib) both → `[2606:4700:10::6814:179a]:443` (example.com). claude → api.anthropic.com, mcp-proxy, Datadog intake. |
| 3b | hostname | **Partly** | uprobe on libc `getaddrinfo` (string arg) | Caught `example.com` for curl and python and `api.anthropic.com` for claude (Bun uses libc). Static binaries and Go's pure resolver would bypass it. No TLS SNI. UDP to 127.0.0.53:53 is visible via `udp_sendmsg`, but the query name is not. |
| 3c | bytes sent/received per connection | **Yes** | `tcp_sendmsg` (size) and `tcp_cleanup_rbuf` (copied > 0); sum by `sock_arg.cookie` | curl: 797 B out / 5,399 B in. python3: 734 / 5,429. claude → API: 127 KB / 233 KB. Costs one event per send/recv call. |
| 4a | kill signals | **Yes** | kprobe `sys_kill` (+ `sys_tgkill`, `sys_pidfd_send_signal`) | run1: claude sent signal 0 (liveness probes) to the other claude pids. run3: `/usr/bin/kill` and bash builtin `kill` → SIGTERM, strace → SIGKILL/SIGSTOP. |
| 4b | ptrace | **Yes** (see caveat) | kprobe `sys_ptrace` | run3: strace's PTRACE_SEIZE/… calls captured. Caveat: in run2 the same hooks with `matchBinaries: /usr/bin/strace, /usr/bin/kill` emitted nothing, while `setresuid` with `matchBinaries: sudo` worked. Not resolved; re-check under the claude tree before relying on it. |
| 4c | setuid / sudo | **Yes** | exec event `binary_properties.privileges_changed = PRIVILEGES_RAISED_EXEC_FILE_SETUID` (+ `--enable-process-cred`), kprobe `sys_setresuid`/`sys_setuid` | run2: `sudo -n /bin/true` flagged as a setuid exec with euid 0, and sudo's setresuid calls were captured. |
| 5 | lost-event detection | **Partly** | Prometheus counters `tetragon_observer_ringbuf_events_lost_total`, `…_queue_events_lost_total`, `tetragon_bpf_missed_events_total`, `tetragon_missed_prog_probes_total`, `tetragon_notify_overflowed_events_total`; a summary log line on shutdown | All counters were 0. Shutdown printed "8436 received, 0% events loss". There is no per-event sequence number, so a gap is known to have happened but not where. |
| 6 | filter to agent tree | **Yes** | `matchBinaries` (`In`, `followChildren: true`) in every selector. Filtering happens in-kernel for kprobes and uprobes. Exec/exit stay system-wide. Export filter `ancestor_binary_regex` also exists. | Every one of the 3,534 kprobe/uprobe events in run1 belonged to the claude tree. The two other claude sessions already running were not matched. |

### Planted session (run1) — what showed up

`claude -p "...z.sh, Edit x, rm junk.txt, curl, python script, lsof..."`. All
steps ran (Claude reported success; `x` now ends `FIVE`; `s.py` was the
script).

| Planted action | Seen? | Events (process) |
|---|---|---|
| `./z.sh` | yes | exec z.sh (binary is the script path), its child `ls -la`, exits |
| Edit `five→FIVE` in x | yes | claude: mknod `x.tmp.155005.…`, write, chmod, rename → `x` |
| `rm junk.txt` | yes | exec rm, unlink `proj/junk.txt` |
| `curl -s https://example.com` | yes | exec, getaddrinfo `example.com`, DNS UDP, tcp_connect :443, 6 sends / 18 recvs, close, exit |
| Write tool → `s.py` | yes | claude: mknod/write/rename `s.py.tmp… → s.py` |
| python3 creates settings.json | yes | python3: mknod + write `settings.json` |
| python3 urllib fetch | yes | getaddrinfo `example.com`, tcp_connect :443, bytes, close |
| `lsof -i -P \| head -5` | yes | exec lsof + head. lsof's reverse-DNS UDP sends were also captured. |

**Noise (Claude Code's own housekeeping, all correctly in-tree):** git
status probes (8 execs), embedded ripgrep (`claude` re-exec'd as
`versions/2.1.302 --files …`), `which node`/`node --version`, the bash
wrapper per Bash call (each runs `source …/shell-snapshots/…; eval '<cmd>'`),
`dirname`/`sed`/`grep`/`uname`/`locale` from the shell snapshot, writes to
`~/.claude/projects/…jsonl`, `~/.claude.json`, `/tmp/claude-1000/…/tasks/*.output`
(the Bash tool's output files), session locks, and API/telemetry
connections.

**Volume:** about 18 s of active session produced 3,939 in-tree events: 3,027
`file_open` (1,398 under `/usr/lib`, 777 `/proc`, 216 `/etc`), 206 writes,
~100 path ops, 131 recv, 43 send, 16 connect, 88 exec, 317 exit (bash
forks). Whole file: 6,680 events, **68 MB** of JSON, because each event
carries `process` + `parent` + 11–14 `ancestors` with full argv (the prompt
is repeated in every event). Without ancestors an exec event is about 2.5 KB
(run2).

## TracingPolicies used

In `.claudetmp/tetragon/policies/`, adapted from official examples
(`filename_monitoring.yaml`, `lsm_file_open.yaml`, `tcp-connect.yaml`,
`sys_ptrace.yaml`, `sys_setuid.yaml`, `uprobe-binaries.yaml`). Every selector
carries:

```yaml
- matchBinaries:
  - operator: "In"
    values: ["/home/ubuntu/.local/share/claude/versions/2.1.302", "/home/ubuntu/.local/bin/claude"]
    followChildren: true
```

- `sentient-files`: `security_file_open` (reg), `security_file_permission`
  (reg, mask 2), `security_path_{mknod,unlink,mkdir,rmdir,rename,chmod,chown,truncate}`.
- `sentient-net`: `tcp_connect`, `tcp_close`, `tcp_sendmsg` (size),
  `tcp_cleanup_rbuf` (copied > 0), `udp_sendmsg`.
- `sentient-proc`: `sys_kill`, `sys_tgkill`, `sys_ptrace`, `sys_setuid`,
  `sys_setresuid`.
- `sentient-dns`: uprobe `getaddrinfo` in `/usr/lib/x86_64-linux-gnu/libc.so.6`.

All four loaded through kprobe_multi/uprobe_multi. The only warnings were
harmless type mismatches (umode_t vs uint32). BPF LSM is not enabled on this
kernel (`lsm: false`), so `lsmhooks` policies would not load here. kprobes on
`security_*` work without it.

**The root binary path is the invoked path, not the resolved one.** The root
`claude` exec reported `binary: /home/ubuntu/.local/bin/claude` (the symlink
on PATH). Claude's self-re-execs (ripgrep) report `…/versions/2.1.302`. The
match list must therefore include the launcher path(s). An npm install
(`node …/cli.js`) cannot be matched by binary at all.

## Sample events (trimmed)

Exec (rm), with process/parent/ancestors shortened:

```json
{"process_exec":{"process":{"exec_id":"aXAt…NTUzODk=","pid":155389,"uid":1000,
 "cwd":"/home/…/tetragon/proj","binary":"/usr/bin/rm","arguments":"junk.txt",
 "start_time":"2026-10-17T00:42:24.855790070Z","parent_exec_id":"aXAt…NTUzODg="},
 "parent":{"pid":155388,"binary":"/bin/bash","arguments":"-c \"source …snapshot-bash-….sh … eval 'rm junk.txt' …\""},
 "ancestors":[{"pid":155005,"binary":"/home/ubuntu/.local/bin/claude","arguments":"-p \"Run ./z.sh, …\""}, …]},
 "node_name":"ip-172-26-12-68","time":"2026-10-17T00:42:24.855790070Z"}
```

Edit tool's atomic rename, done by claude itself:

```json
{"process_kprobe":{"process":{"pid":155005,"tid":155020,"binary":"/home/ubuntu/.local/bin/claude"},
 "function_name":"security_path_rename","policy_name":"sentient-files","action":"KPROBE_ACTION_POST",
 "args":[{"path_arg":{"path":"/home/…/proj","permission":"drwxrwxr-x"}},
         {"path_arg":{"path":"/home/…/proj/x.tmp.155005.2214bd2fb165","permission":"-rw-rw-r--"}},
         {"path_arg":{"path":"/home/…/proj","permission":"drwxrwxr-x"}},
         {"path_arg":{"path":"/home/…/proj/x","permission":"-rw-rw-r--"}}]},
 "time":"2026-10-17T00:42:24.411865226Z"}
```

python3 urllib connect, bytes, and hostname:

```json
{"process_kprobe":{"process":{"pid":155409,"binary":"/usr/bin/python3","arguments":"s.py"},
 "function_name":"tcp_connect","args":[{"sock_arg":{"family":"AF_INET6","protocol":"IPPROTO_TCP",
 "saddr":"2600:1f14:…:3466","daddr":"2606:4700:10::6814:179a","sport":38590,"dport":443,
 "cookie":"18446614045328350720","state":"TCP_SYN_SENT"}}]}}
{"function_name":"tcp_sendmsg","args":[{"sock_arg":{…,"dport":443,"cookie":"18446614044909157376"}},{"int_arg":517}]}
{"process_uprobe":{"symbol":"getaddrinfo","args":[{"string_arg":"example.com"}],"policy_name":"sentient-dns"}}
```

Exit (non-zero; `status` is omitted when 0):

```json
{"process_exit":{"process":{"pid":155024,"binary":"/usr/bin/git"},"status":1},"time":"2026-10-17T00:42:15.050702010Z"}
```

## Resource footprint (measured)

Sampled every second from `/proc/<pid>` (`.claudetmp/tetragon/run1/usage.txt`):

| | RSS | CPU (one core = 100%) |
|---|---|---|
| idle, policies loaded, 59 s | 46–48 MB | 0.8% |
| during planted session, 18 s | peak 63 MB | 10.8% |
| after session | 52 MB | ~0% |

- Flags used: `--execve-map-entries 8192 --process-cache-size 16384`, down
  from 32768/65536, to save memory. BPF maps are kernel memory, not RSS. The
  execve_map at 8192 entries was 7 MB.
- The docs' systemd example shows 151.7 MB, without workload detail.
- Lowest MemAvailable during the run was 219 MB of 1.9 GB, with three other
  Claude Code sessions resident. No OOM pressure from Tetragon.
- Disk: 68 MB of JSON for one 18 s session with ancestors on. This is the
  number to fix (see gaps).

## Standalone operation (no install)

`sudo ./usr/local/bin/tetragon --bpf-lib ./usr/local/lib/tetragon/bpf/
--tracing-policy-dir <dir> --export-filename <file> --server-address 127.0.0.1:54321
--health-server-address '' --metrics-server 127.0.0.1:2112` works from the
extracted tarball. Notes:

- It needs root and BTF (`/sys/kernel/btf/vmlinux`, auto-detected).
- It writes outside its directory regardless of flags:
  - `/run/tetragon/` (pid file, `tetragon-info.json`, and a **cgroup2
    mount** at `/run/tetragon/cgroup2`). All are left behind on exit; I
    unmounted and removed them by hand.
  - `/sys/fs/bpf/tetragon` pins, which are released on clean exit.
- A gRPC unix socket path over ~100 chars fails (`bind: invalid argument`).
  Use a short path or TCP localhost.
- The packaged install (`install.sh`) is a systemd unit plus
  `/etc/tetragon/tetragon.{conf,tp}.d`. That is what a `sen install` would
  wrap.
- On start it scans `/proc` and emits synthetic `process_exec` events for
  every running process (flag `procFS`). That is how pre-existing processes
  get exec_ids.

## Gaps and how we'd fill them

| Gap | Fill |
|---|---|
| A claude started before Tetragon (or before a policy reload) is not followed | Run Tetragon at boot (systemd), before any session. For sessions it missed: sentient finds live claude pids at startup and adds a policy with `matchPIDs` + `followForks` over gRPC, and records a `monitor gap` for the time before. |
| Binary matching is path-based (symlink path; npm = `node`) | Match the launcher paths from `readlink`/PATH at install time, plus `versions/*`. For npm installs, match `node` with `matchArgs`, or skip in-kernel filtering for those hooks and filter in sentient by exec_id ancestry. |
| No per-event sequence number | Poll `/metrics` (or read counters via gRPC) every few seconds. Any increase in the loss counters, a Tetragon restart (new `tetragon-info`/pid), or a gap in the gRPC stream becomes a `monitor gap` alert. Increase `--rb-size-total` from the 128 KB default. |
| `file_open` noise (shared libs, `/proc`, locale) | `NotPrefix` selectors for `/usr/lib`, `/usr/share`, `/proc`, `/sys`, `/etc/ld.so.cache` on `security_file_open`. Optionally use `rateLimit` on the Post action to dedupe repeated opens. |
| Huge events with ancestors | Turn ancestors off and build the tree ourselves from `exec_id`/`parent_exec_id` (cheap: exec events are few). Use `--field-filters` to drop `parent`/argv from kprobe events. Consume over gRPC (`GetEvents`) with the `github.com/cilium/tetragon/api` Go module instead of the JSON file. |
| Hostname only via libc `getaddrinfo` uprobe | Good enough for curl, python, node/Bun and git. Add a uprobe for the libc in other paths. For static/Go binaries, fall back to the IP plus a reverse map built from DNS answers (would need a `udp_recvmsg`/skb parse; deferred). Tracee's DNS packet events are the alternative. |
| File content/diffs | Out of Tetragon's scope (as with eslogger). On write/rename events in-tree, sentient reads the file (size-capped) as planned in design.md. |
| ` (deleted)` suffix on create paths; atomic tmp+rename edits; omitted zero exit status | Normalization in the collector adapter, with fixtures from run1. |
| ptrace/kill with binary selectors didn't fire in run2 | Re-test ptrace under the claude-tree selector on the Mac/Linux box. Fallback: no selector on these low-volume hooks, and filter in sentient. |

## Tracee (doc-level comparison, not run)

| | Tetragon | Tracee (aquasecurity/tracee) |
|---|---|---|
| License | Apache-2.0 (BPF parts GPL-2.0) | Apache-2.0 (BPF parts GPL-2.0) |
| Maturity / cadence | Cilium sub-project (CNCF). Releases 1.4 (2025-03) → 1.5 → 1.6 → 1.6.1 → 1.7 (2026-04) → 1.7.1 (2026-08). ~100+ commits in the last 3 months. ~5.1k stars. | Last release v0.24.1 (2025-11-19), almost a year ago, though commits continue. ~4.6k stars. Pre-1.0. |
| Standalone | Tarball + systemd unit, documented. Verified here. | Single binary or container. Documented for hosts. |
| Event model | Generic kprobe/uprobe/tracepoint/LSM hooks you define in TracingPolicy YAML. Process lifecycle built in. | Hundreds of predefined events (syscalls, `security_file_open`, `magic_write`, `sched_process_exec`, net packet events incl. **DNS and HTTP parsing**) plus behavioral signatures. Less writing of hooks, more choosing of events. |
| Tree filtering | `matchBinaries`/`matchParentBinaries` with `followChildren`, `matchPIDs` with `followForks`, in kernel. | `--scope` filters incl. `follow` (descendants), comm/binary/pid, in kernel. |
| Output | JSON (protojson of `GetEventsResponse`), gRPC with a versioned protobuf API (buf breaking-change checks in CI), Go API module. | JSON/table/Go template outputs, webhook. gRPC server in newer versions. Event schema has changed between 0.x releases. |
| Lost events | Prometheus counters | Stats/metrics for lost events (per docs) |
| Kernel | 4.19+, BTF (CO-RE) | 5.4+ recommended, BTF (CO-RE) |
| Footprint | Measured: ~48 MB idle | Generally reported heavier (larger default event set and maps). Not measured. |

Tracee's advantage is built-in DNS/HTTP parsing, which would close our
hostname gap. Its disadvantages for us are a release that has stalled for
about a year, a pre-1.0 schema, and a heavier default footprint. Tetragon's
narrower "you write the hooks" model suits a collector that wants exactly
our six requirements and nothing else.

AgentSight (eunomia-bpf/agentsight, MIT, ~750 stars) targets AI agents
specifically (SSL uprobes for prompts plus process/file events). It is a
younger, research-grade project and was not evaluated here.

## Sources

- Repo and releases: https://github.com/cilium/tetragon, https://github.com/cilium/tetragon/releases/tag/v1.7.1
- Docs (fetched at v1.7.1 into `.claudetmp/tetragon/docsrc/`): selectors
  (`matchBinaries`/`followChildren`, `FileType`), argument types
  (`file`/`path`/`dentry`/`sock`), events and export filters
  (`ancestor_binary_regex`, `pid_set`, field filters), metrics (loss
  counters), FAQ (kernel 4.19+, BTF), package install.
  https://tetragon.io/docs/
- Example policies: https://github.com/cilium/tetragon/tree/v1.7.1/examples/tracingpolicy
- gRPC API: https://tetragon.io/docs/reference/grpc-api/ and `api/v1/tetragon/*.proto`
- Tracee: https://github.com/aquasecurity/tracee, https://aquasecurity.github.io/tracee/
- AgentSight: https://github.com/eunomia-bpf/agentsight
- Local captures: `.claudetmp/tetragon/run1/` (`events.json`, `tree.jsonl`,
  `metrics.txt`, `usage.txt`, `claude_out.txt`), `run2/`, `run3/`, `policies*/`, `scripts/`.

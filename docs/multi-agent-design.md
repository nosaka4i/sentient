# Supporting Codex beside Claude Code: design

**Status:** proposed, not yet built. The requirements and the decisions behind this are in [multi-agent-support.md](multi-agent-support.md); what Codex writes, and the experiments, are in [ideas/codex-support.md](ideas/codex-support.md). Backlog `8819508`. Nothing is coded until the open questions at the end are answered or accepted as risks.

## Context

On 2026-11-09 a scratch Codex session ran with sentientd and Tetragon on. The log side was readable (JSON lines, one file per session), and the kernel saw every command, but sentient stored none of it: the collector keeps only the process tree of a recognized agent, the recognized agents are Claude's paths (`policy.AgentPrefixes`), and Tetragon's file hooks are limited to the same list, so Codex produced exec and exit events and no file events. The pieces that are Claude's are few and sit in known places. This says how to make each of them agent-aware without changing what Claude shows.

## The mechanism

Five parts, each small. File paths are today's.

### 1. A registry of supported agents (`internal/agents`, new)

One place that knows each agent. Today the knowledge is spread over `policy.AgentPrefixes`, `claudecode.Agent`, `background.IsToolRun` and the Claude paths in `daemon.go`.

```
Agent {
  Name        "claude" | "codex"            // stored in sessions.agent
  Label       "Claude Code" | "Codex CLI"   // what pages show
  LogFiles    func(homes) []string          // transcript files to read
  Binaries    func(homes) []string          // exact binary files installed now (the policy lists these)
  IsAgent     func(binary) bool             // a process of this agent (prefix rules)
  IsHelper    func(binary, argv) bool       // the agent's binary running as a helper, not a session
}
```

The registry is code (decision 3): each agent finds its installed versions itself, as the Claude paths do. Adding an agent is a file in `internal/agents` and an ingester package. `daemon.Run` starts one ingester per registered agent and the collector, policy and attribution ask the registry.

### 2. Pieces pulled out of the Claude ingester

- `SplitCommands` (`internal/ingest/claudecode/shell.go`, which parses a command line into simple commands with `mvdan.cc/sh`) moves to a shared package so both ingesters count commands the same way ("Counting commands" in [design.md](design.md)). `claudecode.SplitCommands` stays as a one-line forward so its tests and callers do not change.
- The file-tailing loop (`ingestFile`: offsets in `ingest_files`, batches of 200, the first-scan and bulk-import logging, pausing while the disk is full) is agent-neutral. It moves to `internal/ingest/tail` and takes a per-agent `Handle(ctx, tx, path, line)`. `claudecode.Ingester` keeps its public methods and calls it.

This is the one refactor of Claude code. It changes no behavior and is its own slice (see "Order of work").

### 3. The Codex ingester (`internal/ingest/codex`, new)

**Files.** `<home>/.codex/sessions/*/*/*/rollout-*.jsonl`, for each home sentientd reads.

**Records.** Every line is `{timestamp, ordinal, type, payload}`. All are stored raw, hash-chained, as written; unknown `type`/`payload.type` pairs are counted and shown, as for Claude (requirement 8). What each known record becomes:

| Codex record | sentient |
|---|---|
| `session_meta` (`id`, `cwd`, `cli_version`) | the session: `agent = "codex"`, `agent_session_id = id`, `cwd`, repo root resolved by sentient (not taken from the log), `agent_version = cli_version` |
| `event_msg` / `item_completed`, item `UserMessage` | a prompt; text is the joined `content[].text` |
| `item_completed`, item `CommandExecution` | one action per simple command of the script (`command[2]` of `["/bin/bash","-lc",script]`), resource `command`; part 0 carries the whole script as `call_command`; `tool_use_id` is the item `id`; the time is the completion time minus `duration`; failed when `status` is `failed` or `exit_code` is not 0 (on the last part only, as for Claude); the working directory is a `file://` URL and is normalized |
| `item_completed`, item `FileChange` | one action per file in `changes`: `add` is create, `update` is update (rename when `move_path` is set), with added and removed lines counted from `unified_diff` |
| `item_completed`, item `Extension` of kind `web.search` | a web search action, which like Claude's server-side search does not count as a system action |
| `response_item` / `function_call` named `spawn_agent` | a subagent-start action |
| `turn_context` | the sandbox and approval mode (part 6 below) |
| `reasoning` (encrypted), `token_count`, `token_usage_record`, `world_state`, `task_*`, `additional_tools`, assistant `message` | stored raw only; known, so not counted as unknown |

**Sessions, resume and subagents.**
- A resumed session appends to its file, so it needs nothing special: the stored offset continues it.
- A subagent has its own file, whose `session_meta` carries `parent_thread_id`, `agent_path`, `agent_nickname` and `subagent_history_start_ordinal` (54 in the sample). Records before that ordinal are a **replay of the parent's history**; they are stored raw (the file is evidence as written) but not turned into actions, or the parent's work would count twice. The records from that ordinal on are the subagent's own.
- The subagent's actions go **into the parent's session**, as Claude's subagent files already do (`sessionFromPath` maps a subagent file to its parent's session). This refines requirement 3, which said "its own session, linked to the parent": a separate session would split one prompt's work over two. The nickname and `agent_path` go in the action summary so the page can say which subagent did it. A nested subagent folds into the **root** session: its `parent_thread_id` is the subagent above it, so a small table `agent_threads(agent, thread_id, root_thread_id)` (part of the migration in part 6) records each thread's root as its file is first read, and survives a restart.

### 4. The kernel side

- **Policy (`internal/collector/tetragon/policy`).** `sentient.yaml.tmpl` already takes its binaries from `{{template "agents" .}}`; the list becomes the union of `Binaries(homes)` over the registry. Codex's is the resolved `~/.codex/packages/standalone/releases/<version>/bin/codex` for each installed version (Tetragon reports the resolved file, not the `~/.local/bin/codex` link). `sen policy --check` (`check.go`) reports a stale list per agent; its text and its notion of an agent root become agent-neutral.
- **Collector (`internal/collector/tetragon/normalize.go`).** `see` keeps a process when `registry.IsAgent(binary)`, replacing `policy.IsAgent(prefixes, …)`.
- **Attribution (`internal/attribute/attribute.go`).** Two changes. A root is an agent process whose parent is not one and that is not a helper (`IsHelper`): today `background.IsToolRun` does this for Claude's ripgrep, and for Codex the `codex` binary also runs inside its sandbox (`codex --sandbox-policy-cwd …`, `--codex-run-as-process-setup`) with `bwrap` as its parent, so without `IsHelper` every command would look like a new agent. And `SessionsActive` gets the agent, so a Claude process in a folder where a Codex session is also active is never tied to the Codex session.
- **Plumbing is not agent activity (`internal/background`).** Each Codex command runs behind `codex-run-as-process-setup`, `codex-linux-sandbox`, `bwrap`, the `codex` helper and a shell that sources a snapshot. A new background rule group, with `RulesVersion` raised, marks these as the agent's own plumbing: they get the existing background treatment (not a prompt's action, not an unexplained kernel-only action).
- **Merge (`internal/merge`).** Unchanged in principle. A Codex command is a wrapper shell, `/bin/bash -c "<script>"`, whose script equals the logged `call_command`, and its parts are the shell's children; that is the shape `mergeCalls` already pairs. The logged `-lc` and the kernel's `-c` do not matter, because the match is on the script text. The snapshot shell (a second `bash -c` per command) is plumbing and must not be taken as a free shell: it is excluded by the plumbing rule above. `matcherVersion` stays, so settled Claude calls are not looked at again. When two Codex commands have the same script and folder, they are paired by **nearest time** (a logged command and its kernel exec were about 35 ms apart in the one sample measured), not only in order as Claude's equal calls are; only commands that start within that distance of each other, in different sessions of one folder, are ambiguous and are marked so. To be checked on more data before the 35 ms is relied on.

### 5. Pages and Health

`sessions.agent` already exists, so a page shows the agent's `Label` per session. The places that say Claude today (`web/templates/health.html`, `layout.html`, `internal/web/agents.go`, `internal/web/health.go`, `internal/query/{agents,health,prompts}.go`) take the label from the registry. Health lists every registered agent with its version, log location and "monitored" (every supported agent is monitored by default; turning one off is the deferred feature, requirement 20). sentientd logs at start which agents it is monitoring.

**Flagging.** The existing rules in [rules.md](rules.md) apply to Codex's actions unchanged. `outside-repo-write` works from the absolute paths in `FileChange` records (so also in agent logs only mode); a write made by a shell command is only seen by the kernel's file events, which need Codex in the policy (part 4). Because Codex's `workspace-write` sandbox cannot write outside its project folder unless a command was escalated, an outside write by Codex means an escalation or a sandbox that is off. A new rule is proposed: **`sandbox-escalation`**, raised when the log shows a request to run outside the sandbox ("Codex asked to run a command outside its sandbox", with the justification quoted), and raised again as confirmed when the kernel sees the command without `bwrap` above it. Severity to be set with the other rules.

### 6. Storage

One migration: `sessions.sandbox_mode` and `sessions.approval_policy`, set from `turn_context` (`sandbox_policy.type` such as `workspace-write`, and `approval_policy` such as `never`); the latest value wins. Pages show them as a small label on a Codex session. No other table changes: `actions`, `prompts`, `call_links` and the evidence tables are already agent-neutral.

## Order of work

Each slice is green on its own and checked in before the next.

1. **Registry and shared pieces,** Claude only. Extract `SplitCommands` and the tail loop, add `internal/agents` with Claude's entry, route the policy, collector and attribution through it. No behavior change; every existing test passes.
2. **Codex log ingester, log side only.** Fixtures from the scratch sessions (decision 5), the ingester, the migration, page wording. Codex then works in agent logs only mode, which also works on a Mac.
3. **Codex on the kernel side.** The registry's Codex binaries in the policy, the plumbing rules, attribution by agent, then the scratch experiment again with `tetra` to confirm the merge pairs commands and file events appear.
4. **Sandbox and approval label,** and a mark for an escalation, once the log shows one (open question 3).

## Why this way, not that

- **A registry, not `if agent == "codex"` branches.** The Claude-specific facts are in five places; a branch in each would make a third agent five more.
- **One ingester per agent, sharing only the tail loop and the command splitter** (decision 2). A mapping file would have to express the subagent replay, the `file://` working directory and the item types, and would be harder to test than plain Go. The tail loop and `SplitCommands` are the parts that really are the same.
- **Match on the script text, not on `process_id` or the whole argument list.** Codex's `process_id` is its own number (1,347 to 96,369 in the sample, against kernel pids above 364,000). The logged `-lc` differs from the kernel's `-c`. The script is the same on both sides.
- **Fold a subagent into its parent's session,** as Claude does, not a session per subagent. Pages already show subagent work inside the prompt that started it.
- **Skip the replay at the start of a subagent file by its ordinal,** not by comparing records with the parent's. The file says where the replay ends, so no content comparison is needed.
- **Plumbing through the existing background rules,** not a Codex-only filter in the merge. The merge already treats "the agent's own background processes" separately; the Codex helper chain is that kind of thing.
- **Built-in binary lists,** not a config file or a flag (decision 3). The config file does not exist yet (backlog `39957d7`) and an agent that needs paths typed in does nothing out of the box.
- **Resolved binary paths in the policy,** because Tetragon reports the file that was executed, and Codex's path carries its version, so the list is refreshed by `sen policy` after each update. This is the same weakness Claude has; Codex updates faster.

## Open questions

1. **`subagent_history_start_ordinal` semantics (answered 2026-11-09).** A run with a nested subagent (a subagent that starts one of its own, `codex exec`, three files) confirmed it at both depths. For the depth-1 file (start 22), 16 of its 22 earlier records appear in the parent's file and only 1 of its 43 later ones does; for the depth-2 file (start 35), 29 of the 35 earlier records appear in the depth-1 file and 1 of the 18 later ones does. The few earlier records not found are the replay's own headers (`session_meta`, task markers). So records before the ordinal are replay and records from it on are the subagent's own, at every depth. Two details the design now needs: (a) a depth-2 subagent's `parent_thread_id` is the depth-1 subagent, not the root (the root appears only in `agent_path`, `/root/inspect_calc/read_module`), so folding a subagent into "the parent's session" means walking the chain to the root; (b) a depth-2 file replays the depth-1 agent's history, which itself begins with the root's replay. The replay is not ordinal-for-ordinal identical to the original (the ordinals are renumbered), so the boundary is the only safe test, not a comparison with the parent's file.
2. **The interactive session's process tree (answered 2026-11-09).** Traced with an interactive Codex 0.167.0 in a detached tmux, running `sleep 47`. The command's ancestry was: `sleep` ← `codex-linux-sandbox` ← `bwrap` ← `codex-linux-sandbox` (the arg0 copy under `~/.codex/tmp/`) ← **`codex app-server --listen unix`** ← `codex app-server daemon pid-up` ← init. The `codex` TUI the user typed into is **not an ancestor**: in interactive mode the commands run under a long-lived app-server daemon (binary `~/.codex/packages/app-server-daemon/releases/<version>/bin/codex`, a different file from the `standalone/releases/<version>/bin/codex` that `codex exec` runs as), which was already running before the session and serves every session. Consequences for part 4: (a) the policy and the collector must list the app-server binary as well as the standalone one; (b) the tree root is the daemon, not a per-session process, so attribution cannot tie a root to one session the way it does for Claude; it must attribute each command, using the project folder the sandbox helper carries in its arguments (`codex-linux-sandbox --sandbox-policy-cwd <dir> --command-cwd <dir> …`) and the log's time window; (c) the daemon is long-lived and started before sentientd, so `sen policy --check`'s "agent started before the policy loaded" warning applies to it and children of a pre-existing process are not followed until Tetragon and the daemon are restarted. In `codex exec` runs the chain starts at the `codex` process itself, so both shapes must work.
3. **How an escalation is logged (answered 2026-11-09).** Sampled with an interactive Codex 0.167.0 (`--sandbox workspace-write --ask-for-approval on-request`): a `curl` was blocked by the sandbox (no output), Codex asked to run it outside the sandbox with a reason, and the owner's terminal approved it once. What the log holds: **no approval event and no marker on the command.** The `CommandExecution` item is identical to a sandboxed one (same fields, `status: completed`, `exit_code: 0`). The only trace is the request, in the `custom_tool_call` `exec` input, which is a JavaScript snippet: `tools.exec_command({cmd: "…", sandbox_permissions: "require_escalated", justification: "May I run curl outside the sandbox…"})`. Whether it was approved is not recorded; it can only be inferred from the command having run (a denial was not sampled). `turn_context` records `approval_policy: on-request` and the sandbox type per turn. So reading the log gives the request and its justification by parsing the snippet text, which is fragile. The stronger evidence is the kernel's: a command that ran outside the sandbox has no `bwrap` or `codex-linux-sandbox` above its wrapper shell, unlike every sandboxed command (sandboxed chain seen in all earlier traces; the unsandboxed chain was **not** traced in this sample, so this is the next thing to verify). **Decided (owner, 2026-11-09): both layers.** In every mode, including agents-logs-only (the Mac), slice 4 shows the log's request and justification, worded as what the agent reported ("asked to run this outside its sandbox"), since the log cannot say it was approved. Where a kernel collector is on, it additionally shows "confirmed ran outside the sandbox" when no `bwrap` sits above the command. Without a kernel signal, nothing is marked unverified, as in the existing log-only rules; if the kernel was down at that moment the command is "not compared". Still unsampled: a denied request.
4. **Several modes in one session.** Storing the latest sandbox and approval mode loses a change made mid-session. Acceptable for a first label; per-prompt storage is the alternative.
5. **Cost of scanning many session files.** Codex starts a file per session, per day, so the folder grows; the tail loop stats every file each poll. Fine now; a heavy user may need directory pruning (skip days with no recent writes).
6. **Helpers named by text.** The snapshot shell is recognized by its script beginning `__CODEX_SNAPSHOT_OVERRIDE`. A Codex update that renames it would show the shell as a free shell until the rule is updated; the Health "unexplained kernel actions" notice would say so.

## Related and deferred

- [multi-agent-support.md](multi-agent-support.md): the requirements; [ideas/codex-support.md](ideas/codex-support.md): the experiments.
- [tetragon-integration.md](tetragon-integration.md): the collector, attribution and merge this extends; [rules.md](rules.md): the flagging rules (the same rules apply to Codex's actions).
- Deferred: choosing which agents are monitored and leaving out projects (backlog `28de833`, needs the config file `39957d7`); a third agent (Gemini CLI suggested); editor agents; macOS and Windows kernel collection for Codex.

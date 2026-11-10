# Supporting more than one agent: requirements

**Status:** proposed requirements, not yet built. Decisions 2, 3, 4, 5 and 7 are made (see the end); decision 1, the third agent, and decision 6, leaving out projects, are open. Written from the Codex experiments in [ideas/codex-support.md](ideas/codex-support.md). The design comes after this; no code before it. Backlog `8819508`.

## Context

sentient shows what a coding agent did, from the agent's own log, and on Linux compares it with what the kernel saw. It was built for Claude Code. The database is already agent-neutral in shape (`sessions.agent`, `prompts`, `actions`, raw records kept as written), but the ingester, the list of agent binaries (in the collector and in the Tetragon policy) and some page wording are Claude's. The experiments show Codex CLI, a terminal agent that writes session logs, can be read with the same approach, and what differs. This is what a second agent must do.

## Goals

- A user who runs Codex sees the same things sentient shows for Claude Code: sessions, prompts, the commands and file changes the agent made, their results, and (on Linux with a kernel collector) which of them the kernel confirms.
- Adding a third agent later is mostly an ingester and a few lists, not a change to the pages or the accounting.
- Claude Code support does not change or get worse.

## Not goals

- Editor agents (Cursor, Windsurf, Cline, Copilot in an editor), Gemini CLI, Aider, opencode, and cloud agents: not examined, not in this step.
- Windows and macOS kernel collection for Codex: the log side should work wherever `~/.codex` is; the kernel side is Linux (Tetragon) first, as for Claude.
- Agents talking to each other (backlog `3c14daa`) and a central server over many users (`f8cabac`).
- Choosing which agents are monitored, at start or while running, and leaving out projects (requirement 20, decision 6): a later feature of its own.
- Showing Codex's reasoning: it is encrypted in the log.
- Codex's cloud tasks (they run elsewhere and leave no local log), and any older Codex CLI that wrote a different log format (I believe the first CLI was a TypeScript program replaced by the current Rust one; not checked). Codex's editor extension and desktop app may share `~/.codex` and so work with the same ingester; unverified, to be tested before it is claimed.

## Requirements

### The log side

1. **Read Codex's session logs** (`~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`) into the same tables, with `agent = "codex"`, keeping every raw record as written and hash-chained, like Claude's.
2. **Incremental and restart-safe,** like the Claude ingester: remembers its position per file, reads only what was appended, and survives a restart or a long pause.
3. **One session per Codex thread.** A resumed session appends to its file and stays one session. A subagent has its own file that starts with a replay of its parent's history; its actions belong to the parent's session (as Claude's subagent files already do; refined in [multi-agent-design.md](multi-agent-design.md)), and the replayed history is not counted twice.
4. **Prompts and actions are filled from Codex's records:** the user's messages as prompts; each shell command as an action with its working directory, exit code, duration and output; each file change (`FileChange`) with its path and diff; each web search with its query; each subagent start.
5. **Failures are shown as failures.** A command with a non-zero exit is a failed action with the message. A refusal by the sandbox looks like any failed command in the log, so the page must not claim the sandbox refused it unless the message says so.
6. **Show the permission mode of a run** (the sandbox and approval policy Codex logs per turn), and show an escalation to run outside the sandbox when the log records one. Should-have: Claude's log has nothing like it, so this is new.
7. **Agent-neutral wording.** Pages, Health tiles and counts name the agent per session and say "the agent" where they mean any; nothing says "Claude" about a Codex session. Health lists each agent with its log location and version.
8. **Unknown records are counted, not dropped,** as for Claude (the Health "unknown record types" notice), so a new Codex version that adds record types shows up.

### The kernel side (Linux with a collector)

9. **Recognize Codex's processes as an agent.** The collector keeps the process tree of a Codex run; the agent's binary path carries its version (`~/.codex/packages/standalone/releases/<version>/bin/codex`), so the match is by prefix.
10. **The Tetragon policy covers Codex too,** so file and network events from Codex's tree are recorded, not only its process starts and exits (today only exec and exit are, since the policy's hooks list Claude's binaries). The list of agent binaries comes from one place and can be extended without a code change. Two things make Codex harder than Claude here: the policy lists **exact binary files** (Tetragon cannot follow children from a prefix match), and Codex's binary path carries its version and Codex updates itself often (0.165.0 to 0.167.0 in a day), so a generated policy goes stale at each update; `sen policy --check` already reports a stale list for Claude, and its wording and its test for "an agent that started before Tetragon" (`internal/collector/tetragon/policy/check.go`) are Claude's and must cover Codex. Codex has several binaries (the `codex` under `packages/standalone/releases/<version>/bin/`, a copy under `app-server-daemon`, the bundled `bwrap`), so which to list is a design question. Codex's own state under `~/.codex` (SQLite files, a 67 MB plugin catalog cache) may add file-open noise at the `essential` level; to be measured before a rule is added.
11. **Match a logged command to its kernel exec by the script text,** not the whole argument list: the log says `bash -lc "<script>"` and the kernel's exec is `bash -c "<script>"`, behind a chain of helper processes (`codex-run-as-process-setup`, `codex-linux-sandbox`, `bwrap`, a shell-snapshot shell).
12. **The helper processes are not agent actions.** They must not appear as unexplained kernel-only actions or as commands the agent ran.
13. **A command Codex ran as a file write** (a heredoc or an inline script) is confirmed by the exec of the shell and, once requirement 10 holds, by the file event on the path.

### Accounting and honesty

14. **The accounting keeps adding up** for Codex sessions: `AgentLog = Confirmed + Unverified + NotChecked + Pending + NotCompared`, and a gap marks Codex actions "not compared" like any other.
15. **Never claim more than was checked.** A file read by Codex is a shell command (`cat`, `sed`); the kernel confirms the command, not "a read tool call". The wording on the pages must match what was actually compared.
16. **Agent logs only mode still works** for Codex: shown as the agent reported it, with no kernel marks.

### Quality

17. **Tests:** fixtures built from sanitized or scratch Codex sessions (never real work), unit tests at 70% coverage or above per package, and a test that a Claude session is unchanged.
18. **Docs:** a doc of what sentient reads from Codex (record types and fields used) and one that says how to add an agent.
20. **Deferred, its own feature (backlog `28de833`): choose which agents are monitored, and change it when wanted.** The owner wants it configurable, sometimes on and sometimes off, so it is more than a start-up flag, and it is not part of the first Codex step. Use case from the owner: Codex and Claude are both installed and only Claude is monitored. A per-agent switch (for example `--agents claude`) decides what sentientd does for each supported agent, and a switched-off agent is left out everywhere: its log files are not read, its processes are not kept by the collector, its binaries are not in the Tetragon policy `sen policy` generates, and nothing of it is stored. Health lists every agent sentient knows about with its state, "monitored" or "not monitored (turned off)", so a skipped agent is visible and not silent. The setting is owned by whoever runs sentientd (a flag or a root-owned config), never by the agent's own user. **Default (decided by the owner): every supported agent is monitored.** A newly supported agent is on from the upgrade that adds it, unless it has been turned off.
19. **Support by shape, not by version.** Codex's CLI changes fast (0.165.0 and 0.167.0 were installed a day apart, through its own auto-update), so sentient reads the log by the record shapes it knows and does not pin a version. It records `cli_version` from `session_meta` and shows it on Health, counts record types it does not know (requirement 8), and the Codex doc names the versions the fixtures came from. A version newer than those is shown as untested, not refused.

## Acceptance

Run the scratch experiment in [ideas/codex-support.md](ideas/codex-support.md) (build, fail, fix, rename, a blocked write, a subagent) with sentient on. The result shows one Codex session with its prompts, its commands and file changes, the failures as failures, a linked subagent, and, on Linux, the commands the kernel confirmed, with the counts adding up and no helper process shown as an action. The same run for a Claude session looks as it does today.

## Open decisions (for the owner)

1. **Which agent is second, and third?** Codex first is assumed. Gemini CLI, Aider and Copilot's CLI are the next terminal agents; editor agents are a different kind of work.
2. **One ingester per agent (decided).** Plain Go for each agent, like the Claude one; agents differ too much (Codex's shell-only edits, subagent files, encrypted reasoning) for a generic mapping to stay simple. A mapping file can be reconsidered if many agents make the per-agent packages repetitive.
3. **The agent's binary list is built in per agent (decided).** Each supported agent's code says where its binaries are and finds the installed versions, as the Claude paths do now (requirement 10 needs one place). The config file (backlog `39957d7`) may override it later.
4. **Show the sandbox and approval mode (decided): yes, per session** (requirement 6 is a requirement, not should-have), with a mark when Codex asks to run a command outside its sandbox.
5. **Test data (decided): sanitized sessions from the scratch runs,** trimmed, with paths and ids made generic, kept in `testdata/`. No real work in them.
7. **Default for a supported agent (decided): on.** Every supported agent is monitored by default. Consequence for the first Codex step, which has no switch yet (requirement 20 is deferred): an upgrade that adds Codex starts monitoring it for anyone who has it installed, with no way to turn it off except `--home` (which users) or leaving the kernel side off. The release notes for that version must say so plainly. Worth reconsidering only if the switch cannot be built soon after.
6. **What the user can leave out.** The per-agent switch is now a requirement (20). Leaving out a project or folder is still open: A person may run several agents and not want sentient on all of them, or not on a personal project. Today the controls are coarse: `--home` limits which users' homes are read, `--tetragon` turns the kernel side on or off, and the policy lists the agents installed. There is no per-agent switch (for example `--agents claude,codex`) and no way to leave out a project or folder, and with more agents the first one matters. A per-agent switch is small; excluding a project is a real feature (what is excluded must itself be visible, and the setting must belong to whoever owns sentientd, not to the agent). Backlog `28de833`.

## Related

- [ideas/codex-support.md](ideas/codex-support.md): the experiments and findings this is based on.
- [design.md](design.md), [tetragon-integration.md](tetragon-integration.md), [rules.md](rules.md): the model, the merge, and the flagging rules the second agent must work with.
- Backlog: `8819508` (this), `3c14daa` and `f8cabac` (later products).

# sentient — product brief

## Problem

In long sessions with a coding agent, the user delegates heavily and stops
checking each action. Actions scroll past in the chat, and many are hard to
read: a 400-character `sed -i` or an inline Python script hides what it
actually changed. In practice this becomes blind trust.

From one user's Claude Code history (~19 session files):

- 5,394 shell commands: median 132 characters, p90 438, max 10,226
- ~678 shell commands appear to modify files (`sed -i`, `rm`, `mv`, `>`, heredocs)
- 49 file-editing `sed`/`python` commands longer than 300 characters

A view built only from the agent's explicit edit tools would miss a large
share of real changes.

## Goal

A mechanism to establish greater trust in coding agents:

> See what your coding agent actually changed, what needs your attention,
> and trace any change back to why it happened.

Trust comes from being able to verify cheaply, not from seeing more.

## Two modes

### 1. Trust — glanceable, during or right after a session

Answers "is there anything I need to look at?" in seconds.

- **Effects, not syntax.** Show the diff a command produced, not the command.
- **Net result first.** Summary on top: files changed, created, deleted,
  commits, plus flags.
- **Collapse noise.** Runs of reads and searches become one "Explored" line;
  repeated edits to one file roll up into one combined diff.
- **Triage.** Flag what deserves attention: deletions, shell-made edits,
  changes outside the repo, failed-then-retried commands, network access,
  `git push` / `reset` / `--force`, config and secrets files.
- **Expand on demand.** Every line opens to the exact commands, outputs,
  and per-step diffs. Nothing hidden, only folded.

Example:

```
nosaka · feature/reminder-mvp · Oct 7, 14:02–15:40 · "add snooze to reminders"

NET RESULT   7 files changed (+212 −48) · 2 created · 1 deleted · 3 commits
             ⚠ 2 files changed via shell · 1 command failed · nothing outside repo

14:02  ▸ Explored   read 18 files in packages/server/src/core, ran 3 searches
14:09  ✎ Changed    reminders.ts  +64 −12                         [view diff]
14:11  ▶ Ran        npm test  → ✗ 2 failing
14:14  ✎ Changed    reminders.ts, scheduler.ts  +20 −6            [view diff]
14:15  ▶ Ran        npm test  → ✓ passed
14:20  ⑂ Subagent   "find snooze UI in iOS app" · read 9 files, changed nothing
14:31  ✎ Changed    ReminderRow.swift +41 −3 · created SnoozeSheet.swift
14:33  ⚠ Shell edit sed -i on Config.plist                        [view diff]
14:40  ✗ Deleted    old_snooze_stub.ts  (via rm)
15:38  ● Committed  "Add snooze to reminders" (a1b2c3d)
```

### 2. Forensics — later, when something is wrong

Start from an effect and trace it backward:

```
Config.plist line 12 is wrong
  ← changed by: sed -i '...' (14:33, session 404d4f60)
  ← agent's stated reason: "updating the bundle id to match..."
  ← in response to the user's message: "make the iOS build use the staging server"
  ← preceded by: 3 reads, 1 failed build
```

- Search by file: everything any agent ever did to this file.
- Provenance chain: change → action → agent reasoning → user message.
- Raw records kept next to every summary.

## Design principles

1. **Evidence independent of the agent's account.** Prefer what actually
   happened (filesystem state, git, exit codes) over what the agent said it
   did. Show both side by side.
2. **Deterministic summaries.** Compute summaries, diffs and flags
   mechanically. Any AI-written summary is labeled and linked to evidence;
   a model should not vouch for a model.
3. **Claim vs reality.** Check agent claims ("tests pass", "only touched the
   server package") against the record, and build a track record over time.
4. **Calibrated trust.** Point to the few actions worth checking so it is
   reasonable to skip the rest.
5. **A trustworthy tool.** Open source, local-only by default, read-only
   toward projects, no telemetry.
6. **Agent-neutral.** The UI never depends on which agent produced an event.

## Data sources

The logs are written by the agent's harness, not the model, so support is
per tool, not per LLM. Formats are undocumented and change without notice.

**Claude Code** (verified locally):

- Sessions: `~/.claude/projects/<launch-dir-with-dashes>/<session-id>.jsonl`,
  one JSON event per line.
- Folders are keyed by the directory `claude` was launched in, not by repo.
  The folder name is lossy; use the `cwd` field in each record instead.
- Each record has `sessionId`, `uuid`, `parentUuid` (provenance chain),
  `timestamp`, `cwd`, `gitBranch`.
- Subagents: `<session-id>/subagents/agent-<id>.jsonl` (same `sessionId`,
  `isSidechain: true`) plus `agent-<id>.meta.json` with `agentType`,
  `description`, and the spawning `toolUseId`.
- Large tool outputs are stored separately in `<session-id>/tool-results/`.
- Transcripts are deleted after ~30 days by default (`cleanupPeriodDays`).
- `gitBranch` has been seen to disagree with the repo at `cwd`; don't trust
  it without checking.

**Others** (to verify): Codex CLI (`~/.codex/sessions/`), Gemini CLI,
Cursor, others.

## Event model

```
Event {
  id, agent, session_id, parent_id,      // parent_id → provenance chain
  subagent_id?,
  timestamp,
  cwd, repo_root, branch,                // repo_root resolved by us, not the agent
  kind: "command" | "file_read" | "file_write" | "file_edit" | "file_delete"
      | "web" | "subagent" | "user_message" | "agent_message" | "other",
  target,                                // path, command, or URL
  detail,                                // diff, output, etc.
  status,                                // ok | error | denied
  effect_source: "tool" | "inferred" | "observed",
  raw_ref                                // pointer to the original record
}
```

`effect_source` says how a change is known: from an explicit edit tool,
inferred by parsing a shell command, or observed from filesystem snapshots.

## Known gaps and how to close them

| Gap | Approach |
|---|---|
| Shell commands that change files | v0.1: flag likely-modifying commands. v0.2: before/after snapshots per command, or a filesystem watcher during live sessions. |
| Changes outside the repo | Filesystem watcher; flag absolute paths outside `repo_root`. |
| Log retention and tampering | Copy events into our own append-only store as they happen. |
| Format drift | Adapter per agent, tested against fixture files; fail loudly on unknown record types. |

## v0.1 scope

- Claude Code adapter only, including subagents and offloaded tool results.
- Indexer into SQLite, with repo root resolved from `cwd`.
- Trust timeline view per session: net result summary, collapsed steps,
  flags, expandable diffs and commands.
- File view: every file touched, with diffs, across sessions.
- Shell commands that probably modified files are flagged, never silently
  left out.
- Responsive web UI served locally (phone access via Tailscale or SSH tunnel).

Built for forensics from day one, even though the forensics UI comes later:
provenance links stored, raw records kept, events indexed by file.

## Later

- Before/after snapshots for shell commands; filesystem watcher for live sessions.
- Forensics view: trace from file/line back to the user message.
- Claim-vs-reality checks.
- Codex CLI and Gemini CLI adapters.
- Live capture via hooks (e.g. Claude Code `PostToolUse`).
- Git correlation: which agent changes ended up in which commits.

## Open questions

- Is "the log says `sed -i` ran on X" enough, or is the actual diff required
  from v0.1?
- Should changes outside the project directory be in scope?
- Personal visibility only, or evidence a team or reviewer can rely on
  (tamper-resistance)?
- Stack: TypeScript end to end (`npx sentient`) vs a single Go binary.

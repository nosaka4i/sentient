# Supporting Codex CLI beside Claude Code: what we found

**Status:** brainstorm and experiment notes, not requirements and not a design. Nothing here is built. Backlog `8819508`. The next step in the process is a requirements doc, written after the open experiments below.

Sentient reads Claude Code's logs and compares them with what the kernel saw. This records what OpenAI's Codex CLI writes, so that the decision to support a second agent, and the model it needs, rests on observation. The data is from real sessions on one machine; where something was not tested it says so.

## Context

sentient was built around Claude Code: `internal/ingest/claudecode`, one agent name (`claude`), a list of Claude binary paths (`AgentPrefixes` in `internal/collector/tetragon/policy/policy.go`), and wording on the pages that says Claude in places. The database is already agent-neutral in shape (`sessions.agent`, `prompts`, `actions`, raw records kept as written), so a second agent is mainly an ingester and a few lists. Codex CLI is the closest second candidate: a terminal agent that writes session logs to disk. Other candidates (Gemini CLI, Aider, opencode, Copilot, and the editor agents such as Cursor) are in the backlog item; none was examined.

## How the data was gathered

- Codex CLI **0.165.0** installed under `~/.local/bin` on the Linux dev box, free account, signed in with `codex login --device-auth` (plain `codex login` needs a browser callback on the same machine, which a headless box does not have).
- Sessions were run in the git-backlog repository (a different repo from sentient), first read-only, then with a task that wrote files.
- Three looks at the same session file as it grew: 92, 207, then 464 records (15 prompts, 38 commands, 4 file changes, 4 web searches). Early looks read structure only (record types and field names with `jq`); the later ones read values, with the owner's permission.
- **Kernel side not tested.** sentientd was not running during the first session. When it ran later, its database held no Codex processes, as expected: Codex is not in `AgentPrefixes`. Tetragon's own output (`tetra getevents`) was not captured.

## Where Codex keeps things

Everything is under `~/.codex`; nothing was added to the repository Codex worked in.

| path | what |
|---|---|
| `sessions/YYYY/MM/DD/rollout-<time>-<session id>.jsonl` | the session log, one file per session, in a folder per day |
| `history.jsonl`, `session_index.jsonl` | prompt history and an index of sessions |
| `logs_*.sqlite`, `goals_*.sqlite`, `memories_*.sqlite`, `queue_*.sqlite` | Codex's own state; not examined |
| `config.toml` | settings (the `[tui]` table, and a `trust_level` per project) |
| `rules/default.rules` | saved approvals, one `prefix_rule(pattern=[...], decision="allow")` per command prefix |
| `auth.json` | the sign-in; a secret, never read |
| `shell_snapshots/`, `skills/`, `cache/`, `packages/` | the shell environment, built-in skills, caches, and Codex's own binaries |

## The session log

JSON lines. Every record has `timestamp`, `ordinal`, `type` and `payload`; some have `metadata`.

| record `type` / `payload.type` | what it is |
|---|---|
| `session_meta` | once per file: session and thread ids, `cwd`, `cli_version`, `originator`, `source`, `model_provider`, git `branch`, `commit_hash` and `repository_url` |
| `turn_context` | per turn: the model, `cwd`, `sandbox_policy`, `approval_policy`, permission profile |
| `event_msg` / `task_started`, `task_complete`, `turn_aborted` | the turn's life; `turn_aborted` appeared twice, when the user interrupted |
| `event_msg` / `item_completed` | the finished things: `UserMessage`, `AgentMessage`, `Reasoning`, `CommandExecution`, `FileChange`, `Extension` |
| `event_msg` / `token_count`, `token_usage_record` | token use and rate limits |
| `response_item` / `message`, `reasoning`, `custom_tool_call`, `custom_tool_call_output`, `function_call`, `function_call_output` | the model's side of the exchange |
| `world_state` | a snapshot of the environment, repeated |

Parts that matter to sentient:

- **Commands.** A `CommandExecution` item has `command` (the **full argument list**, such as `["/bin/bash", "-lc", "cat README.md"]`), `parsed_cmd` (read, list_files, or unknown), `cwd`, `duration`, `exit_code`, `aggregated_output`, `status`, `source` (`unified_exec_startup`) and a `process_id`.
- **`process_id` is not the kernel's pid.** The values in the sample run from 1,347 to 96,369 with no order over time, while real pids on the machine were above 364,000. It is Codex's own id. Matching a logged command to a kernel process cannot use it.
- **The argument list is the likely match key.** It should equal the kernel's exec argv. This is simpler than Claude Code, where a wrapper shell carries the command as text and the match is by normalized text. **Not tested** against kernel data.
- **File changes.** A `FileChange` item has `changes`, an object keyed by **absolute path**; each value has `type` (`add` or `update`) and `unified_diff` (update) or `content` (add), plus `move_path`; the item also carries `stdout` ("Success. Updated the following files: M path") and `status`. The diff is logged directly. Claude Code logs the edit's old and new strings instead. A kernel file create or write on the same path could confirm one.
- **Web access.** An `Extension` item of kind `web.search` records the query and the results. In the sample Codex searched for the latest version of an action and read the public GitHub API for the repository's workflow runs. That is network activity by the agent that the log states.
- **Reasoning is encrypted** (`encrypted_content`), so the log cannot show the agent's reasoning. Claude Code's log is closer than it first looks: its `thinking` blocks carry a signature and, mostly, no text (212 of 2,621 in one session had any, at most 568 characters), so for both agents the reasoning is largely not recoverable. What Claude's log does have in readable form is the agent's visible messages to the user, and so does Codex's (`AgentMessage`).
- **Permission context is logged.** Each turn records the sandbox and approval policy, so a page could say how freely a run was allowed to act. Claude Code's log does not record this.
- **Tools.** See the next section.

## Codex's tools (compared with Read, Edit, Write)

Codex has far fewer file tools than Claude Code. The session log lists the tools available (the `additional_tools` record, and a listing Codex printed for itself); the list the model printed was truncated by Codex, so it may be incomplete. In the sample only three were actually called: `exec_command` 43 times, `web__run` 4 times, `apply_patch` 4 times.

| Codex tool | what it does | nearest Claude Code tool |
|---|---|---|
| `exec_command` | runs a shell command in a PTY; options for the working directory, a per-command **sandbox override** (`require_escalated` runs unsandboxed and asks for approval), and a reusable approval prefix | Bash |
| `write_stdin` | sends input to a command that is still running | none |
| `apply_patch` | edits files with a freeform patch: add, update, move, delete | Edit and Write together |
| `view_image` | reads a local image | Read (for images) |
| `web__run` | web search and fetch (logged as `web.search`) | WebSearch, WebFetch |
| `create_goal`, `get_goal`, `update_goal` | a long-running objective with a token budget | none |
| `clock__curr_time`, `sleep` | the time; waiting | none |
| `list_mcp_resources`, `read_mcp_resource`, `list_mcp_resource_templates` | MCP resources | MCP tools |
| `spawn_agent`, `followup_task`, `send_message`, `wait_agent`, `list_agents`, `interrupt_agent` | start and steer other agents | Agent (subagents) |
| `request_user_input` | ask the user | AskUserQuestion |
| `mcp__codex_tui__*` | manage Codex tasks: create, fork, read, archive and title threads; create and list worktrees | none |
| `mcp__codex_apps__*` | ChatGPT-side apps: document control, plugin and permission management, "pets" | none |

What follows for sentient:

- **There is no read tool.** Reading a file is a shell command (`cat`, `sed -n`, `rg`), which Codex's own log classes as `read` in `parsed_cmd`. A Claude Code file read is a tool call that the kernel confirms with a file-open event; a Codex file read is an exec the kernel sees as a process. The "file read confirmed" accounting would have to work from the exec for Codex.
- **All writes outside the patch tool are shell too** (`>` redirects, `sed -i`, `git`, build output). Only `apply_patch` is logged as a `FileChange`; a file written by a shell command appears only as a command, like Claude Code's Bash.
- **Sandbox escalation is explicit.** `exec_command` can ask to run unsandboxed (`require_escalated`) with a justification. That is a security-relevant event the log carries and Claude Code's does not; worth showing.
- **Subagents exist** (`spawn_agent` and the rest). Not seen in the sample, and the shape of their records is unknown.
- **Codex runs its tools through a JavaScript `exec` wrapper** (a `custom_tool_call` named `exec` whose input is a snippet that calls `tools.exec_command(...)` and so on). The logged `CommandExecution` and `FileChange` items are the useful records; the snippet itself is not.

## Processes on the machine

A Codex session is more than one process: the `codex` TUI the user ran, a background `codex app-server --listen unix` daemon (with a `daemon pid-up` helper), and a `codex-code-mode-host`, all under `~/.codex/packages/app-server-daemon/releases/<version>/bin/`. The daemon outlives the session and is a child of init, not of the TUI. Commands ran as `/bin/bash -lc "<script>"`. So the process tree is not rooted at one agent binary the way Claude Code's is, and which process is "the agent" for attribution is open.

## Claude Code and Codex, side by side

| | Claude Code (built) | Codex CLI (observed) |
|---|---|---|
| log | `~/.claude/projects/<project>/<session>.jsonl` | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` |
| session identity | file name, session id in records | `session_meta.id`, thread and turn ids |
| prompts | `user` records | `UserMessage` items |
| command | Bash tool call, command text, result later | `CommandExecution`, argv array, result in the same item |
| matching to the kernel | by normalized text, via a wrapper shell | by argv (likely), not tested |
| file edits | Edit and Write tool inputs (old and new strings) | `FileChange` with a unified diff |
| agent's reasoning | `thinking` blocks: mostly empty text plus a signature (in one 2,621-block session, 212 had readable text, at most 568 characters); `redacted_thinking` (encrypted) none seen | `reasoning` items: `encrypted_content` only, empty summary |
| web access | a tool call | `web.search` item with query and results |
| permission mode | not in the log | in `turn_context` |
| version | in records | `cli_version` in `session_meta` |
| subagents | `subagents/` files | not seen; `turn_context` has a `multi_agent_version` field, not explored |
| the process | one binary path, versioned directory | TUI, app-server daemon, code-mode host |

## What sentient would need (observations, not a design)

- An ingester for the Codex log, writing the same `sessions`, `prompts` and `actions` tables with `agent = "codex"`, and keeping raw records as written (hash-chained).
- The agent list for the kernel side to include Codex's binaries, and a rule for which of the processes above is the root of an attributed tree.
- Page wording and the Health "agents" tiles that do not say Claude where they mean the agent.
- A decision on which Claude-shaped fields are universal. Reasoning text, subagents and tool names are not.

## Not known yet

- **The kernel side:** what Tetragon records for a Codex session, whether the argument list equals the log's `command`, and whether file changes line up with kernel file events.
- **Resumed and forked sessions:** how a resumed session appears (one file, or several).
- **Failures and refusals:** a command blocked by the sandbox or denied at the approval prompt, and how each is logged.
- **Subagents and multiple agents** (`multi_agent_version`).
- **`codex exec`** (non-interactive): whether it writes the same log.
- **An unexplained field:** `session_meta.source` said `vscode` while `originator` said `codex-tui`.
- **Other agents:** Gemini CLI, Aider, opencode, Copilot's three forms, and the editor agents (Cursor, Windsurf, Cline). Editor agents run their own loop and write their own logs, so Claude Code ingest does not cover them even when they use Claude models.
- **Windows and macOS locations** of `~/.codex`.

## Second experiment: a session with the sandbox working (2026-11-09)

Run from Claude Code's shell with `codex exec` (and `codex exec resume --last`) in a scratch git repository under `.claudetmp/`, Codex 0.167.0 (it updated itself from 0.165.0 during the day), `--sandbox workspace-write`, while `tetra getevents -o compact --timestamps` appended to a file. Five prompts: build a Go module and tests, break a function and fix it and rename files, commit and try a write outside the folder, start a subagent, and a web search. It took about 90 seconds. The sandbox only works on Ubuntu 24.04 after allowing `bwrap` user namespaces (see "Running Codex from Claude Code's shell"); the first attempt ran every command into a sandbox failure and produced no useful records.

What the log and the kernel showed:

- **A resumed session appends to the same file.** The four prompts of one session are four `task_started` / `task_complete` pairs in one `rollout-*.jsonl`. A new `codex exec` makes a new file.
- **`codex exec` writes the same records** as the interactive session (`originator` is `codex_exec` and `source` is `exec` in `session_meta`; the record types are the same).
- **Failures are ordinary failed commands.** A `CommandExecution` with `status: failed`, a non-zero `exit_code` and the message in `aggregated_output`. A write outside the repository ended `Read-only file system` (exit 1); `git add` and `git commit` ended `Unable to create .git/index.lock` because **the sandbox keeps `.git` read-only** (exit 128). Nothing in the record says "the sandbox blocked this"; only the message text does. A command that simply finds nothing also fails (`rg --files` in an empty folder: exit 1).
- **Codex wrote its files through the shell,** not `apply_patch`: `cat > file <<'EOF'` and `python3 -` with an inline script. So there were **no `FileChange` records**. File edits then appear only as command text (and a later `cat` or test run), the way Claude Code's Bash edits do. `apply_patch` is a possibility the model chooses, not what Codex always does.
- **Subagents get their own session file.** `session_meta` in it has `thread_source: subagent` and `source.subagent.thread_spawn` with `parent_thread_id`, `depth`, `agent_path` (a name for the task) and `agent_nickname`. The file starts by **replaying the parent's history** (several `task_started` and `task_complete` pairs in the same millisecond, and a `turn_aborted`), then holds the subagent's own turn (8 seconds here). The parent's file records the `spawn_agent` call.
- **One command is a chain of processes.** For each command the kernel saw: `/proc/self/exe --codex-run-as-process-setup`, then `codex-linux-sandbox`, then `/usr/bin/bwrap`, then `codex --sandbox-policy-cwd …`, then a `/bin/bash -c` that sources a shell snapshot, then `/bin/bash -c "<the script>"`, then the program (`python3`, `git`, `rm`, `go`). Four or five helper processes surround every command.
- **The logged argument list is not the kernel's.** The log says `["/bin/bash", "-lc", "<script>"]`; the kernel's exec says `/bin/bash -c "<script>"` (no `-l`). So the match key is the **script text** (the third element), not the whole argument list. The time lines up closely: the log's item time was 35 ms after the kernel's exec for `rm text_test.go`.
- **The Codex binary's path carries its version:** `~/.codex/packages/standalone/releases/<version>/bin/codex`, like Claude's versioned directory. The same `codex` binary also runs inside the sandbox helper, and the background daemon is under `~/.codex/packages/app-server-daemon/releases/<version>/`.
- **The kernel saw exec and exit for all of it, but no file events.** None of the scratch files' opens, creates or the refused write were in the capture. sentient's Tetragon policy limits every file hook to the listed agent binaries ("Every hook is limited to the agent binaries below and what they start", in `sentient.yaml.tmpl`), and the list has only Claude's. So today the kernel cannot confirm a Codex file read or write, even when sentientd keeps the exec records. Adding Codex means changing the policy's binary list as well as the collector's.
- **`tetra getevents -o compact` has no pid or parent,** only the binary and a joined argument string. JSON output has them, and sentient's collector uses the API, not this text.

What stays unknown: an approval prompt, a denial, and a command that asks to run outside the sandbox (these need an interactive session, and a free account to spare); resumed sessions that fork; whether `apply_patch` records match what the notes above say once the model uses it again (they did in the first session, with `FileChange` and a unified diff).

## What the collector does with Codex today

Nothing is stored. The collector keeps only the process tree of a recognized agent (`internal/collector/tetragon/normalize.go`), and a process is an agent when its binary path matches `AgentPrefixes`, which lists Claude's paths only. Tetragon still reports Codex's processes; sentient drops them. Checked on 2026-11-09: sentientd running, 330 `claude` processes stored in the last hour and none from Codex. The list has no flag or setting, so seeing Codex through sentient needs a code change (an option for extra agent binaries would be the smallest one); until then the kernel's view of Codex is only visible with `tetra getevents`.

## Experiments still to run

1. Run sentientd and `tetra getevents` during a Codex session that makes file changes, and compare argv and paths with the log.
2. Resume a session and see what is appended or created.
3. Make a command fail, be blocked by the sandbox, and be denied at the prompt.
4. Run `codex exec` once.
5. Look at one session that uses subagents (`spawn_agent`, `wait_agent` and the rest): how the subagent's work is recorded, whether in the parent's file or its own, how it is linked to the parent, and what processes it runs as. The prompt is `.claudetmp/codex-prompts/05-subagent.md` (scratch work only; the prompts are not committed).

### Running Codex from Claude Code's shell

Tried on 2026-11-09 with `codex exec --sandbox workspace-write` from the Bash tool: Codex reported that shell commands could not start ("the sandbox cannot create a loopback interface") and that file writes were denied, so nothing ran. Cause: Codex's Linux sandbox uses `bwrap --unshare-net`, and creating that network namespace's loopback address fails with `Operation not permitted` (`bwrap: loopback: Failed RTM_NEWADDR`) when it runs inside Claude Code's own shell sandbox; `unshare --user --net` alone works, so user namespaces are allowed and only the loopback setup is refused. Run from a normal terminal, the same Codex sandbox works. So Codex sessions that Claude Code drives need either Claude Code's sandbox lifted for that command or Codex's own sandbox disabled, and each choice changes what confines the run.

## Questions for the requirements doc

- Which agents come second and third, and why that order?
- One ingester per agent writing a common model, or a generic reader driven by a mapping file per agent?
- What does sentient claim for an agent whose reasoning it cannot read?
- Does the "unverified" accounting work the same when the argv, not text, is the match?
- How is a background daemon that outlives the session attributed?

## Related

- [design.md](design.md), [tetragon-integration.md](tetragon-integration.md): the model and the merge this would extend.
- Backlog `8819508` (this), `f8cabac` (a central server over many users' agents, which makes a second agent matter more).

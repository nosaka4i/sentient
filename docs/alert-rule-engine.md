# Alert-rule engine

**Status:** proposed, not yet built. Requirements: [rules.md](rules.md) (the rule list, the wording rules and the decided open questions). Backlog: `7129a04`.

## Context

The alerts page lists and reviews rows of the `alerts` table (`internal/query/alerts.go`, `internal/web/alerts.go`), but nothing writes them. The rules exist only as a spec. Three things made it worth building the structure before the rules:

- **A real incident:** an agent (or something it ran) edited the owner's `~/.bash_profile` without anyone noticing. Nothing today says "an agent changed a startup file", and nothing says which program did it.
- **Deletes are the commonly reported agent failure** (production databases, whole folders removed with `rm`). The only rules written for it were Low and covered tracked files in the repo, the recoverable ones.
- **The Mac credential rule had nothing to attach to.** Its work (Mac collector step 3) is blocked until an engine exists, and the Linux rules need the same engine.

Real data to tune against exists: `.claudetmp/dev/kdata/sentient.db` has 33 sessions, 5,499 prompts and 44,309 kernel unlink events. It showed that raw counts are useless (95% of unlinks were Go build temp files) and that one agent process spans days, so "the prompt of the process" is the wrong unit for counting.

The principle that shapes everything below (owner, 2026-11-18): **the more noise sentient produces, the less useful it is.** Every alert has to be worth reading.

## The mechanism

### Where it runs

A new package, `internal/rules`, with an `Engine` run beside the merger (`internal/daemon/daemon.go` starts `Attributor` and `Merger` the same way). It runs after the merge, not in the collector's sink stream, because a rule needs what only the merge knows: the prompt, whether the log claimed the action, the session's repo. A kernel event arrives before its process is attributed; a stream evaluator would see it too early.

The engine reads two stores of facts and keeps a cursor for each (`rule_cursor`: source, last id, rules version):

- `kernel_events` (file create, write, unlink, rename, open; exec), joined to `processes` and `process_attribution` for the program, its arguments, and the session and prompt.
- `actions` (what the log claims, plus the kernel-only commands `merge.kernelOnly` makes), with their `evidence`.

Both are turned into one Go struct, `Fact`: source, time, session, prompt, process binary, whether that binary is the agent's own, resource, action, path, second path, arguments, evidence, and whether the user typed it (see open question 3). Rules only ever see a `Fact`, so they do not care which table it came from or which platform produced it.

### Zones

A rule is mostly "this kind of action in this kind of place". The places are **zones**, defined once as path lists in the rules file, with `~`, `{repo}` and per-platform entries (`/Library/Keychains` only on the Mac):

| zone | examples |
|---|---|
| `system` | `/etc`, `/usr` (not `/usr/local`), `/System`, `/Library` |
| `secrets` | `~/.ssh/id_*`, `~/.aws/credentials`, keychains, `*.pem`, `.env*` outside the repo |
| `startup` | `~/.zshrc`, `~/.bashrc`, `~/.bash_profile`, launch items |
| `agent_log` | the agent's session records (`~/.claude/projects/**`, `history.jsonl`, `sessions/**`) |
| `personal_data` | mail, calendar, reminders, notes and contacts stores (later) |
| `scratch` | `/tmp`, `/private/tmp`, `/private/var/folders`, gitignored folders at the repo root such as `.claudetmp` |
| `inside_project` | under the session's working folder |
| `outside_project` | everything else: the base zone |

Zones are ordered, and **the first zone that matches a path wins**, so a secret inside the project is `secrets`, not `inside_project`. This is the layering: `outside_project` is the baseline, the special zones are stricter cases of it, and a rule on a special zone never needs to repeat the "outside the project" condition. All paths are compared through `paths.Spellings` (`/tmp/x` equals `/private/tmp/x`).

### A rule

Rules are data in one YAML file embedded in the binary (`internal/rules/rules.yaml`, `go:embed`, parsed with `gopkg.in/yaml.v3` at start). A rule has the fields of [rules.md](rules.md): `id`, `severity`, `title`, `why`, `group`, plus `kind` and `match`:

```yaml
- id: shell-startup-write
  kind: match            # the default; count, sequence and first_seen are reserved
  severity: high
  match:
    zone: startup
    action: [file_update, file_create, file_unlink, file_rename]
    by: any              # any | not_agent_binary
  title: "Agent edited your shell startup file ({process})"
  why: "It runs in every new shell, not just this project."
  group: prompt_rule     # one alert per prompt per rule
```

Conditions that YAML cannot say are **named checks**, small Go functions registered by name (`match.check: recursive_rm`, `git_force_push`, `untracked_in_project`). A check receives the `Fact` and returns whether it matches. This is the escape hatch: most rules are data, the hard ones (parse a command, ask git whether a file is tracked) are code the rule names. A new check is a code change, a new rule over existing zones and checks is a one-line data change.

Title and `why` use the placeholders in [rules.md](rules.md) (`{path}`, `{file}`, `{process}`, `{count}`, ...).

### Kinds

Only `match` (one fact meets the conditions) is built first; all starter rules fit it. The format reserves the others so adding one changes no existing rule and no table:

- `count`: N or more facts of a kind in one prompt (`bulk-delete`, parameter `threshold:`).
- `sequence`: A then B in one prompt or session (a secret read, then a connection to a new host).
- `first_seen`: something never seen before from this agent (`new-host`).

The engine records per-prompt counts from the start (`prompt_counts`: prompt, key such as `delete` or `upload_bytes`, count), so `count` rules can be tuned from real numbers before any is switched on. Counting uses **each fact's own time inside the prompt's window**, not the prompt of the fact's process: one agent process lives for days and would put all of its housekeeping under one prompt.

### Alerts: grouping, merging, wording

- **One alert per prompt per rule.** `alerts` gets columns `session_id`, `prompt_id`, `kernel_event_id` (kernel facts have no action), `group_key`, `count`, `fields` (JSON: path, process, ...), `title`, `why`, `rules_version`. Repeats update the count and "+N more" paths of the existing alert (`UNIQUE (prompt_id, rule, group_key)`), so the pass is idempotent.
- **One alert per act.** When one act matches several rules (a recursive delete that is also a bulk delete), the alert carries the highest severity and the other matches go to `alert_matches` (alert, rule, severity) as detail.
- **Title and `why` are stored rendered**, as the alert was raised: an alert is a historical record, and a later rule edit must not change what it said.
- **Evidence is never touched.** A rule only creates an alert; disabling or changing a rule deletes nothing recorded.

### Re-checking when a rule changes

`rules_version` (a constant beside the YAML, like `background.RulesVersion`) goes up with every rule change. When the cursor's version differs from the current one, the engine resets the cursors and evaluates everything again; the unique key makes that safe. Alerts raised under an older version by a rule that no longer matches are left as they are, marked by their stored version.

### Exemptions

- The agent's own binary touching its own state (`~/.claude`) is plumbing, not an alert: rules on `agent_log` use `by: not_agent_binary`.
- The agent's known start-up credential lookups are not alerts either: they are a plumbing group in `internal/background` ("Claude Code login lookup"), listed and counted on the agent's page.
- **Commands the user typed with `!` raise no alerts at any severity.** The log stores them as a user message `<bash-input>...</bash-input>` with no `tool_use` block, unlike an agent's Bash call.

## Why this way, not that

- **Rules as embedded data, not Go code.** The existing `background.rules` are Go functions. That needs a release for a one-path change, and the list cannot be read or reviewed as a list. Data keeps the list auditable and lets a user file add rules later; hard conditions stay in named checks.
- **Not Sigma or Falco rules.** Both are built for other event shapes (generic logs, syscall streams). Our match is over our normalized `Fact` with zones, prompts and claim-versus-record evidence; translating would cost more than the small format below. The prior art is still the model for the field names.
- **After the merge, not inline in the collector's sink.** Inline sees a kernel event before it has a prompt, a claim or a repo. The alert would be raised without them or never updated.
- **YAML, not TOML.** Both read well; the owner finds YAML easier for long path lists. It costs one Go module.
- **A user rules file now: no.** Allowlists and user-defined rules wait until v1 has shown where the noise is. The file must be root-owned in the data folder, because the agent runs as the user and could edit a file in the repo or home to silence its own alerts (backlog `6a4542b`). The loader is written to accept a second file later.
- **Counting by the process's prompt: no.** One long-lived agent process was blamed for 688 deletes in one prompt over 23 hours; time windows are right.
- **Rendering the title at display time: no.** See above; alerts are records.
- **Alerting on reads of the agent's logs: no.** `~/.claude` holds what the agent is meant to read (`CLAUDE.md`, settings, skills, memory), and broad searches from the home folder touch it, so a read rule would fire constantly. Writes, renames and deletes by another program are the rule.

## First slice

Engine skeleton and the migration (alert columns, `alert_matches`, `prompt_counts`, `rule_cursor`); `Fact` from `kernel_events` and `actions`; zones; the `match` kind; named checks; the rendered title; and four rules that exercise the structure: `shell-startup-write` (zone plus action), `agent-log-tamper` (zone plus `not_agent_binary`), `secret-read` (zone, with the commands as a check) and `system-write`. Tests run on Linux against fixtures and the stand-in database. Next: `recursive-delete` and `untracked-delete` with the `rm` checks, then `count` for `bulk-delete`.

## Open questions

1. **Attribution lag.** A kernel fact may be evaluated before its process is attributed. Wait up to a grace period for the prompt, or raise the alert at once and fill the prompt in when it is known?
2. **Mac zone paths.** The Mac entries (keychains, Reminders, Calendar and Notes stores, launch items) come from the spec and one prototype capture, not from tests on a Mac.
3. **`!` commands need the ingester.** Nothing reads the `<bash-input>` message yet. The ingester must record such commands as user actions, and the merge must match them to their kernel execs, before the exemption can work.
4. **Kernel-only file changes.** `actions` holds only the log's file calls and kernel-only commands. `unclaimed-write` needs the kernel's file changes that no log call claims, as facts. The first slice reads `kernel_events` directly for this.
5. **Scratch-folder recognition.** "Gitignored, at the repo root" is untested as a rule for finding folders like `.claudetmp`.
6. **Load.** A first pass over a large `kernel_events` table (835 MB here) must not stall `sentientd`; the cursor and batch size need measuring.

## Related and deferred

- Count, sequence and first-seen rules; `large-upload` (needs network byte counts, which the Mac does not capture yet).
- User-defined rules and allowlists (`6a4542b`); the scratch-directory view (`a70bbf2`); the adversarial test of the rules (`2a7ac0a`); the plain-language view for non-coding users (`ec05b0a`); other agents (`ad25681`).
- The Mac plumbing rules and the login-lookup group (Mac collector step 3, slice B): [mac-collector-design.md](mac-collector-design.md).

# sen status

**Status:** mockup, not yet built

Alerts first, then a summary of activity in the window, then the window's prompts. Reads the store the background monitor writes; current to the second, not a live capture.

```
$ sen status
History from Oct 6 09:12 (1d 6h ago)

NEEDS ATTENTION (4)
#    DATE          AGENT    REPO     SEVERITY   RESOURCE   ACTION    PATH
43   Oct 7 14:52   claude   nosaka   high       file       update    ~/.zshrc
41   Oct 7 14:33   claude   nosaka   medium     file       update    ios/Config.plist
44   Oct 7 15:02   claude   nosaka   medium     file       update    reminders.ts, scheduler.ts +1 more
42   Oct 7 14:40   claude   nosaka   low        file       delete    old_snooze_stub.ts

LAST 24H
RESOURCE    ACTION    COUNT   SUMMARY
prompt      request   4       2 repos · agent worked 1h 58m
file        read      214     38 outside repo
file        create    2
file        update    9       +248 −61
file        delete    1
command     execute   41      2 failed
process     inspect   3       lsof, ps
network     connect   5       github.com, registry.npmjs.org, localhost:3000 · ↓38 MB ↑12 KB
git         commit    3
web         search    1
subagent    start     2

PROMPTS (4)
    DATE          AGENT    REPO          SESSION   DURATION   ACTIONS   FLAGS   PROMPT
●   Oct 7 15:30   claude   git-backlog   9c1e      12m+       18        1       add a --json flag to list
    Oct 7 15:20   claude   nosaka        404d      6m         14                run the tests and commit
    Oct 7 14:45   claude   nosaka        404d      12m        37        1       make the iOS build use the staging server so that the TestFl…
    Oct 7 14:02   claude   nosaka        404d      36m        112       2       [pasted 240 lines] this crash log is from the reminder scr…

  sen alert <#>            details for an attention item
  sen session <id>         prompts in a session
```

When capture was interrupted, that becomes the first attention item:

```
#    DATE                AGENT   REPO   SEVERITY   RESOURCE   ACTION   PATH
45   Oct 7 14:02–14:05   —       —      high       monitor    gap      —
```

## Notes

- Top line says where the data starts: "History from <date> (<age> ago)" — a range from then to now. It was "Monitored from", but the start is the oldest imported log record, and logs are backfilled: sentient may have started watching long after (on a Mac, a 103-day-old log made it say monitored for 103 days on the first day). "Monitored from" is kept for later, when kernel capture can say when sentient first watched a session. Avoid "recorder".
- Sections are tables, columns separated by tabs (Go `text/tabwriter` aligns them in a terminal; when piped, emit raw tabs so `cut`/`awk` work).
- Every activity is described as RESOURCE + ACTION (+ PATH), audit-log style (cf. Kubernetes verb/resource, CloudTrail event/resource type). Actions are present-tense verbs even though they already happened. Enables filters like `--resource file --action delete`.
- Vocabulary (proposed):

  | Resource | Actions |
  |---|---|
  | file | read, create, update, delete, rename, chmod |
  | dir | create, delete |
  | command | execute (anything the agent ran: sed, curl, grep, npm…; `sudo` is an execute that severity rules flag) |
  | process | inspect (lsof, ps), kill — other running processes the agent acted on |
  | network | connect, listen |
  | web | search, fetch |
  | git | commit, push, force-push, reset |
  | launch item | add (persistence: something set to run after the session) |
  | subagent | start (in Claude Code a subagent runs inside the same `claude` process, more like a thread than a spawned process; its actions are counted in the other rows; telling them apart from the main agent's is log-only evidence) |
  | prompt | request (the user's own messages to the agent; the only user-side row) |
  | monitor | gap (capture was interrupted) |

- PATH: the file path; for other resources the natural identifier (command for process, host for network, URL for web). Several files: first two then `+N more`. Name PATH chosen by the user; may need revisiting for non-file resources.
- Caveat: Endpoint Security does not report TCP/IP connections — kernel-level network evidence needs a Network Extension (separate component + entitlement); until then network/web actions are agent-log or command-derived evidence only.
- NEEDS ATTENTION is a quick summary: no command text and no WHY column. How it happened (e.g. via `sed`), why it was flagged and the full command are in `sen alert <#>`.
- NEEDS ATTENTION sorted by severity, then time. Severity is rule-based, not AI-judged: high = not in agent's log, sensitive read then network, sudo, force push, monitor gap; medium = shell-made edits, writes outside repo; low = in-repo deletions, failed commands.
- `#` is the alert's database id (unique, stable forever, like GitHub issue numbers) — so it's out of order when sorted by severity. Handles for actions: `sen alert 43` details, `sen alert ok 41 42` mark reviewed (proposed, not settled).
- If a command must ever be shortened (e.g. in `sen alert`), keep it whole when it fits; otherwise elide script/expression bodies first, never the program, behavior-changing flags (`-i`, `-rf`, `--force`) or targets.
- LAST 24H: a rolling window, not calendar today (so it is not empty first thing in the morning). Heading names the window (`LAST 2 DAYS` with `--since 2d`). Exactly these 11 rows, always shown even with a zero count, so the layout never shifts (same in the web UI). Minor actions fold in: `web` counts search + fetch, `git` counts commit + push, `file` rename goes under update, `dir` create/delete under file create/delete. Risky ones (force-push, reset, chmod, process kill, launch item add, monitor gap) are not rows — they raise alerts and land in NEEDS ATTENTION. All 22 resource/action counts are one step deeper: `sen activity` (web: "All activity →"). SUMMARY holds the one thing that matters per row; no per-item counts, just distinct values. `process inspect` names the programs (`lsof, ps`); `network connect` names the endpoints (`github.com, registry.npmjs.org, localhost:3000`) — the commands that made them are in `sen activity`.
- Network direction: one `network connect` row with bytes both ways (`↓38 MB ↑12 KB`), not separate download/upload actions (every connection sends and receives; classifying by dominant direction is guesswork). Upload alerts key on bytes sent. Byte counts need the Network Extension; until then the row shows endpoints only. Command syntax (`git push`, `curl -T`) can hint direction but is weaker evidence.
- Cause vs effect: `command execute` counts everything the agent ran (the cause); the file/network/process rows are what those commands did (the effect). `npm install` counts in both, like `sed -i` counts as a command and a file update.
- DATE format: `Oct 7 14:52` (local time); year added only when not the current year. Alerts persist until cleared, so a bare time would be ambiguous.
- AGENT column in NEEDS ATTENTION and PROMPTS (matches `sen session`); hardcoded `claude` for now.
- NEEDS ATTENTION and PROMPTS show at most 5 rows (most severe / most recent); the header keeps the real total ("NEEDS ATTENTION (7)") and a footer line says what was cut and where to see it ("… 2 more (sen alert)"). `-n <N>` changes the limit, `--all` removes it. LAST 24H is not limited (fixed small set of categories; cutting it would mislead).
- `sen` alone prints help/usage; `sen status` is the summary. Alias `st`.
- Flags: `--since <dur>` (e.g. `2d`), `--repo <name>`.
- PROMPTS: newest first. SESSION column tells you where to drill in (`sen session 404d` lists that session's prompts, numbered); no prompt-number column (`9c1e 1` / `N` rejected as confusing). FLAGS = attention items the prompt caused; a running prompt can already have flags. Only marker is `●` = still running (`⚠` dropped as redundant with FLAGS). PROMPT is the last column so it takes the remaining width: first line only, whitespace collapsed, pasted blocks shown as `[pasted N lines]`, no quotes, truncated with `…`; full text in `sen session`. Capped at 5 like NEEDS ATTENTION — `sen status` is a glance view; complete views are `sen activity` and `sen session`. Open: whether a complete cross-session prompt list is needed.

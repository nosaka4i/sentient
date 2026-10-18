# sen session

**Status:** mockup, not yet built

`sen session` with no argument lists recent agent sessions, one per line, newest first — like `git log --oneline`. With an id it shows one session (not yet mocked). Alias: `sessions`.

```
$ sen session
ID     AGENT   REPO         STARTED       DURATION  PROMPTS  FLAGS  FIRST PROMPT
● 9c1e claude  git-backlog  Oct 7 15:30   12m+      1               "add a --json flag to list"
  404d claude  nosaka       Oct 7 14:02   1h 24m    3        ⚠ 3    "add snooze to reminders"
  b7a2 claude  rly          Oct 7 10:15   47m       5               "fix the bundle relay bug"
  e03f claude  sentient     Oct 7 09:40   22m       2        ⚠ 1    "read the brief and get up to speed"
  51d8 claude  nosaka       Oct 6 16:05   2h 10m    9               "wire up the reminder scheduler"
  c2f0 claude  git-backlog  Oct 6 11:20   35m       4               "add priority to items"

6 sessions in the last 2 days · 1 running · 4 things flagged
sen session <id>         prompts in a session
sen session <id> <n>     one prompt (add --trace for every step)
```

## Notes

- `●` = still running; `12m+` = ongoing duration.
- ID: 4-char prefix of the agent's session id, lengthened automatically on collision (git-style).
- AGENT: hardcoded to `claude` for now; becomes real when a second agent is supported.
- No agent version column (decided against for now).
- Sessions have no names, so the first prompt identifies them.
- Default window: last 2 days. Flags: `--since <dur>`, `--repo <name>`, `--flagged` (only sessions needing attention).

## Addressing

- `sen session` — list recent sessions (above)
- `sen session 404d` — one session and its prompts, by id prefix
- `sen session nosaka` — latest session in that repo
- `sen session 404d 3` — only the 3rd prompt of that session and what it caused
- `--trace` — every numbered step

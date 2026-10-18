# sen activity

**Status:** mockup, not yet built

The complete record of everything the agent touched: every file read, write and delete, every process it ran or inspected, every network connection — one row per action, nothing folded or hidden. Other views (`status`, `session`) summarize; this one doesn't.

```
$ sen activity
DATE              AGENT    REPO     SESSION   RESOURCE   ACTION    PATH
Oct 7 14:02:10    claude   nosaka   404d 1    file       read      packages/server/src/core/reminders.ts
Oct 7 14:02:11    claude   nosaka   404d 1    file       read      packages/server/src/core/scheduler.ts
Oct 7 14:03:02    claude   nosaka   404d 1    command    execute   grep -rn "snooze" packages/
Oct 7 14:05:40    claude   nosaka   404d 1    dir        create    packages/server/src/lib/
Oct 7 14:05:41    claude   nosaka   404d 1    file       create    packages/server/src/lib/snooze.ts
Oct 7 14:09:12    claude   nosaka   404d 1    file       update    packages/server/src/core/reminders.ts
Oct 7 14:11:03    claude   nosaka   404d 1    command    execute   npm test
Oct 7 14:11:04    claude   nosaka   404d 1    network    connect   registry.npmjs.org
Oct 7 14:12:30    claude   nosaka   404d 1    process    inspect   lsof -i :3000
Oct 7 14:13:05    claude   nosaka   404d 1    process    inspect   lsof -p 51234
Oct 7 14:33:20    claude   nosaka   404d 2    command    execute   sed -i 's/com.nosaka.app/…' ios/Config.plist
Oct 7 14:33:20    claude   nosaka   404d 2    file       update    ios/Config.plist
Oct 7 14:40:02    claude   nosaka   404d 2    file       delete    old_snooze_stub.ts
Oct 7 14:52:44    claude   nosaka   404d 2    file       read      ~/.zshrc
Oct 7 14:52:45    claude   nosaka   404d 2    file       update    ~/.zshrc
...
```

Filtered by resource and action (works for every row of `sen status`'s TODAY table):

```
$ sen activity process inspect
DATE              AGENT    REPO     SESSION   RESOURCE   ACTION    PATH
Oct 7 14:12:30    claude   nosaka   404d 1    process    inspect   lsof -i :3000
Oct 7 14:13:05    claude   nosaka   404d 1    process    inspect   lsof -p 51234
Oct 7 15:31:12    claude   rly      9c1e 1    process    inspect   ps aux
```

## Notes

- Complete by design: answers "what did the agent touch?" with nothing left out. Reads are listed individually here, unlike other views that fold them.
- Same RESOURCE / ACTION vocabulary as [status](status.md).
- Positional filters: `sen activity [resource] [action]`. Other filters: `--since <dur>`, `--repo <name>`, `--session <id>[ <n>]`, `--path <glob>`, `--outside-repo`.
- Times include seconds (many actions per minute).
- A shell edit shows as two rows: the `command execute` and the `file update` it caused (seen by the kernel).
- Output can be thousands of rows: pages through `less` in a terminal, like `git log`; plain tab-separated rows when piped.
- Default window: today. Open: name — `activity` vs `log`.

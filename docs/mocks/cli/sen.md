# sen

**Status:** mockup, not yet built

`sen` alone prints help/usage (standard cobra output). Command names are nouns, leaving room for e.g. `sen file <path>` later.

```
$ sen
sen — see what your coding agent actually did

Usage:
  sen [command]

Available Commands:
  status      What needs your attention, and the last 24h of activity
  serve       Serve the web dashboard on localhost
  version     Print the sen version
  help        Help about any command

Flags:
  -h, --help   help for sen

Use "sen [command] --help" for more information about a command.
```

Built: `sen`, `status`, `serve` (web dashboard on `127.0.0.1:7357`, `--addr`, `--socket`) and `version`; `session`, `activity` and `alert` come later and get added to this help when built. See [status](status.md).

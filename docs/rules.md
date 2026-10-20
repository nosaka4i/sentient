# Alert rules

**Status:** proposed, not yet built

The deterministic rules that raise NEEDS ATTENTION items, and the words each
one shows. See [design.md](design.md) → "Correlator and rules" and "Why
deterministic rules, not a model".

## How a rule is written

Each rule is data, not prose generated at runtime:

| Field | Meaning |
|---|---|
| `id` | stable kebab-case name, shown on the alert page (`shell-startup-write`) |
| `severity` | `high` / `medium` / `low` |
| `match` | resource + action + conditions on path, argv, host, evidence |
| `title` | one plain-English line, starts with "Agent …", may use fields |
| `why` | one sentence: why it matters, no jargon |
| `group` | how repeats collapse into one alert (per prompt, per path, …) |

Fields available to `title` / `why`: `{path}`, `{file}` (basename),
`{dir}`, `{cmd}` (first word of the command), `{argv}`, `{host}`,
`{bytes_up}`, `{bytes_down}`, `{count}`, `{repo}`, `{target}` (git ref /
pid / item).

Wording rules:

- Title says what the agent did, in the user's words: "Agent edited your
  shell startup file", not "file/update on ~/.zshrc matched rule 12".
- The path or command goes in the title only when it's short and is the
  point (`{path}`); otherwise it's on the alert row and page already.
- `why` answers "so what?" for a developer, in one sentence, no
  hedging, no "may potentially".
- Same wording in the web UI and the CLI.

## Rules

### High

| id | Matches | Title | Why |
|---|---|---|---|
| `shell-startup-write` | file update/create of `~/.zshrc`, `~/.bashrc`, `~/.bash_profile`, `~/.profile`, `~/.zprofile`, `~/.config/fish/config.fish` | Agent edited your shell startup file | It runs in every new shell, not just this project. |
| `secret-read` | file read of `~/.ssh/id_*`, `~/.aws/credentials`, `~/.config/gcloud/**`, `~/.kube/config`, `~/.netrc`, `~/.docker/config.json`, `~/.npmrc` with a token, `*.pem` / `*.key` outside the repo | Agent read your {file} | It holds credentials that work outside this project. |
| `pipe-to-shell` | command where a download (`curl`, `wget`) is piped into a shell or interpreter (`sh`, `bash`, `python`, …) | Agent ran a downloaded script ({host}) | Code from the internet ran without being saved or reviewed first. |
| `git-force-push` | git push with `--force` / `--force-with-lease` / `+ref` | Agent force-pushed {target} | It can overwrite commits on the remote that other people rely on. |
| `unclaimed-write` | kernel saw a file write/delete with no matching tool call or command in the agent's log | Agent changed {path} without saying so | The agent's own log doesn't mention this change. |
| `launch-item-add` | file create under `~/Library/LaunchAgents`, `/Library/Launch*`, `~/.config/systemd/user`, `/etc/systemd`, crontab edits | Agent added something that starts automatically ({file}) | It keeps running after the session ends and on every login. |
| `sudo-use` | command execute of `sudo`, `su`, `doas`, `pkexec` | Agent ran a command as root ({cmd}) | Agents shouldn't need root; anything is possible from there. |
| `system-write` | file write under `/etc`, `/usr` (not `/usr/local`), `/System`, `/Library` (not launch items, which have their own rule) | Agent changed a system file ({path}) | System files affect every user and app on this machine. |
| `lib-hijack` | a process loads a shared library from `/tmp`, the repo, or via `LD_PRELOAD` / `DYLD_INSERT_LIBRARIES` | Agent loaded code from an unusual place ({file}) | This is a common way to hide code inside a normal-looking program. |
| `monitor-gap` | sentient lost events or wasn't running | sentient wasn't watching from {start} to {end} | Anything the agent did in that window is not recorded. |

### Medium

| id | Matches | Title | Why |
|---|---|---|---|
| `outside-repo-write` | file write/delete outside the session's repo root (and not covered by a more specific rule; temp dirs and caches excluded) | Agent changed a file outside the project ({path}) | Changes outside the repo don't show up in git and are easy to miss. |
| `contents-replaced` | a file's old contents were replaced without being read first (`>` redirect, truncate) | Agent replaced {file} | The previous contents were overwritten and may not be recoverable. |
| `git-history-rewrite` | `git reset --hard`, `git rebase`, `git commit --amend`, `git push` of rewritten history, `git branch -D`, `git clean -fd` | Agent rewrote git history ({cmd}) | Commits or uncommitted work can be lost this way. |
| `build-config-change` | edits to build / deploy / environment config: `*.plist` build configs, `.env*`, `Dockerfile`, `docker-compose*`, `*.xcconfig`, `vercel.json`, `fly.toml`, Terraform | Agent changed build or environment config ({file}) | It changes where or how the app runs, not just its code. |
| `ci-change` | edits to `.github/workflows/**`, `.gitlab-ci.yml`, `.circleci/**`, `Jenkinsfile` | Agent changed CI ({file}) | CI runs with the repo's secrets on every push. |
| `git-hooks-change` | writes to `.git/hooks/**`, `.git/config`, `core.hooksPath` changes, `~/.gitconfig` | Agent changed git's settings or hooks | Hooks run automatically on your next git command. |
| `chmod-exec-download` | chmod +x on a file that was downloaded in the same session | Agent made a downloaded file executable ({file}) | Downloaded code is about to be run. |
| `new-registry` | package install from a non-default registry or a git/URL dependency | Agent installed a package from {host} | Packages from unusual sources skip the usual checks. |
| `large-upload` | network connect with ↑ above a threshold (default 5 MB) to a non-allowlisted host | Agent uploaded {bytes_up} to {host} | Large uploads can carry your code or data off the machine. |
| `process-kill` | process kill of a process the agent didn't start | Agent stopped a process it didn't start ({target}) | It may have stopped something you were using. |

### Low

| id | Matches | Title | Why |
|---|---|---|---|
| `repo-file-delete` | file delete inside the repo of a tracked file | Agent deleted {file} | Deleted files are easy to overlook in a review. |
| `bulk-delete` | more than N (default 20) files deleted in one prompt | Agent deleted {count} files | Large deletes are easy to miss among other changes. |
| `dependency-change` | edits to `package.json`, `go.mod`, `requirements*.txt`, `Cargo.toml`, `Gemfile`, `Podfile` adding a dependency | Agent added a dependency ({target}) | New dependencies bring code you haven't read. |
| `new-host` | network connect to a host never seen before from this agent | Agent connected to a new host ({host}) | First contact with a host is worth a glance. |

## Health warnings, not alerts

These describe the machine, not something the agent did, so they go on
the Health page (and `sen health`), not in NEEDS ATTENTION:

- Agent account has passwordless sudo.
- Agent runs as root.
- sentientd or the collector is down (the gap itself is the
  `monitor-gap` alert).

## Open questions

- Allowlists: per-repo config for expected hosts, paths and commands
  (backlog: user-defined rules).
- Thresholds (`large-upload`, `bulk-delete`) need real-data tuning.
- Grouping: one alert per prompt per rule, with `{count}` and "+N more"
  paths (as in the dashboard's "reminders.ts, scheduler.ts +1 more"), is
  the starting point.
- `!` commands typed by the user never raise alerts (attributed to the
  user).

# Hardened Linux for agents — brainstorm

**Status: brainstorm, separate project idea (not part of sentient).** Captured
2026-10-17; backlog item `f01190c`. Lives here until it gets its own repo.

## Problem

Coding agents run with whatever privileges their host account has. On the
box sentient is being designed on, Claude Code has passwordless sudo and
open network: it could install anything, read any credential, talk to any
host. The usual safeguard — approving each command before it runs — doesn't
scale: the user ends up approving bash commands they can't evaluate
("I have no idea what I'm approving"), or the work stalls.

The better split is:

1. **Constrain up front** — the agent can only do what's allowed.
2. **Let it run** — no per-command approvals for allowed actions.
3. **Verify after, by exception** — sentient shows the few things worth a look.

Step 1 has building blocks but no turnkey answer. This project is step 1.

## Prior art

- **Anthropic's reference devcontainer** (`.devcontainer/` in the Claude Code
  repo): default-deny iptables + ipset allowlist (GitHub, npm, Anthropic
  API). Meant to be copied and customized; documented as not immune to all
  attacks.
- **agentbox** (Go, MIT): generates devcontainers for Claude Code / Codex
  with domain-level allowlists (dnsmasq for dynamic domains).
- **Trail of Bits devcontainer**: filesystem isolation for audits; network
  isolation opt-in.
- **Claude Code's built-in sandbox** (bubblewrap on Linux, Seatbelt on
  macOS): per-command filesystem/network limits inside the agent.
- **Cloud agent sandboxes** (E2B, Daytona, Modal; Firecracker/gVisor
  microVMs): isolation as a hosted service, aimed at agent builders.
- Teams moving from containers to a VM per worktree after agents collided
  over ports and services.

Gap: no ready-made machine image — boot it, point your agent at a project,
and it's safe by default.

## What it would be

A security-hardened Linux for agents: a sandbox with only what an agent
needs to do its job, and nothing else. A minimal, hardened image built to
host coding agents:

- **Root exists, the agent can't reach it.** The system keeps a root
  account (and/or an admin account) for the human: installs, updates,
  changing the allowlist. The agent runs as an unprivileged user that is
  not in sudoers, doesn't know the root password, and can't `su` (e.g.
  `su` restricted to an admin group via pam_wheel); no setuid helpers it
  can abuse. Admin happens from the host console or a separate login, never
  from the agent's session.
- **Filesystem.** Only the project (and maybe a scratch dir) writable; no
  host home, no host SSH keys or cloud credentials mounted.
- **Network allowlist, default deny.** Model API, git host, package
  registries; everything else blocked and logged. Domain-based (DNS-aware)
  rather than static IPs. Possibly an egress proxy for per-request logs.
- **Credentials.** No long-lived secrets in the image. Short-lived, scoped
  tokens (e.g. a fine-grained GitHub token for one repo); optionally git
  push only via a host-side broker.
- **Resource limits.** cgroups caps on CPU/memory/disk so a runaway agent
  can't take down the host.
- **Undo.** Snapshot before each session/task (VM snapshot, btrfs/ZFS) so
  any agent run can be rolled back wholesale.
- **Observability built in.** sentient preinstalled with kernel capture
  (eBPF/Tetragon), so every session is recorded; blocked attempts become
  alerts.
- **One per task.** Cheap to spin up a fresh instance per worktree/task;
  agents don't collide.

## What's installed

Principle: what agents actually use, nothing that helps escape, hide or
persist. Language toolchains come as optional profiles, not in the base.

- **Base:** kernel, systemd, coreutils, util-linux, bash, ca-certificates,
  tzdata, locales.
- **Agent runtime:** the agent CLI (Claude Code native binary; Node only
  for npm-installed agents), git, gh (optional).
- **Everyday tools agents reach for:** grep, sed, gawk, findutils,
  diffutils, patch, less, file, tree, tar, gzip, xz, zstd, unzip, curl, jq,
  ripgrep, fd, procps (ps/top), make.
- **Toolchain profiles** (chosen per project, installed by the admin):
  python3 + venv/pip, node + npm/pnpm, go, rust, build-essential (gcc) for
  native modules.
- **Security layer (agent can't touch):** nftables + a DNS-aware allowlist
  (e.g. dnsmasq/ipset), sentient + Tetragon, cgroup limits, snapshots
  (btrfs/ZFS or VM-level).
- **Admin only:** sudo or root login from the console/separate account;
  editors (vim/nano) for the human.

Deliberately left out or locked down:

- Network tools: nc/ncat, socat, nmap, tcpdump, telnet, ssh server (admin
  access via the host/console instead).
- Docker/container runtimes (socket = root). If needed: rootless Podman.
- Persistence for the agent user: cron/at denied (`cron.allow`), no
  systemd `--user` lingering.
- setuid binaries beyond the minimum; no GUI.
- System package installs by the agent: apt needs root, so the agent can
  only install into the project (venv, node_modules) from allowlisted
  registries.

## Forms it could take

| Form | Isolation | Notes |
|---|---|---|
| Container image (devcontainer) | weakest (shared kernel) | easiest adoption; what exists today |
| microVM (Firecracker, Cloud Hypervisor) | strong, fast boot | Linux hosts; per-task instances |
| Full VM image (QEMU/UTM/Tart/Lima on Mac) | strong | Mac users run Linux agents locally |
| Cloud image (AMI etc.) | strong | e.g. the user's own setup: agent on a remote box |

Probably: one declarative definition, built into several of these.

## Base OS options

- **Ubuntu/Debian minimal** — familiar, what agents expect (apt, glibc).
- **Fedora CoreOS / immutable OSes** — read-only root, atomic updates;
  agents can't alter the system.
- **NixOS** — the whole image declared in one file; reproducible,
  auditable; steeper learning curve.
- **Alpine** — tiny, but musl breaks some toolchains agents use.

## Threat model sketch

Protects against: an agent (by mistake, prompt injection, or misbehavior)
deleting/altering things outside the project, reading the user's
credentials, exfiltrating data to arbitrary hosts, installing persistence,
escalating privileges, exhausting host resources.

Doesn't protect against: damage inside the project itself (that's what
snapshots/undo and sentient review are for), misuse of the allowed hosts
(e.g. pushing bad code to the allowed git remote), kernel escapes from a
container (use a VM tier).

## Open questions

- Product shape: image + CLI (`agentlinux up ./project`)? A config format
  (allowlist, mounts, tokens) on top of existing VM tools (Lima, Tart,
  Firecracker)?
- How the user's normal workflow fits: editing in VS Code on the host while
  the agent runs in the box (shared folder? git remote?).
- Allowlist UX: start strict and learn from blocked attempts ("allow
  pypi.org for this project?")?
- Credential broker: worth building, or just scoped tokens?
- Relationship to sentient: bundled, or sentient as an optional add-on?
- Name.

## Related

- sentient: [brief](../brief.md), [design](../design.md) — the verify half.
- Design assumption there: agents never get root.

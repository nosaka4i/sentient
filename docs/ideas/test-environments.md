# Test environments: where agent workloads run, and where we capture them

**Status:** working notes, written 2026-11-11 because the sessions are not contiguous and context was getting lost. Not a requirements doc.

## The constraint

The owner is behind the Great Firewall. Claude Code, Codex and Gemini CLI cannot be relied on from the Mac: the VPN is unstable, and a plain proxy would not last (the firewall would find it). sentient needs real agent activity to capture and to test against, on every platform it supports.

## The machines

| machine | can run | reaches the agents' APIs | used for |
|---|---|---|---|
| Linux box (AWS, ssh, 2 GB RAM) | Claude Code, Codex (both run here today), Tetragon | yes | Linux kernel capture, log ingesters, all Go development |
| Mac (M2, 24 GB) | Xcode, `eslogger`, Endpoint Security extension, Ollama | no, or unstable | Mac collector (the thing to build) |
| Windows PC (LAN, 256 GB RAM, RTX 5070 12 GB) | Ollama server | n/a (local) | candidate model server for the Mac, on the same network |

The Linux box cannot capture Mac events, and the Mac cannot drive a real agent. Everything below is about closing that gap.

## What each task needs

1. **Parser and socket reader for the Mac collector:** process and file events from anything that looks like an agent (a stand-in binary). No model, no network.
2. **Real event shapes from Endpoint Security:** captured with `eslogger` (Apple's tool, built on Endpoint Security, no entitlement needed). Script ready: `.claudetmp/es-experiment/activity.sh` plus the capture command; tells us fields, volume, detached processes, writes outside the repo.
3. **End-to-end on the Mac:** a real agent session on the Mac, so the agent log (Claude Code's JSONL) and the kernel events can be matched. This is the one that needs an agent that runs.

## Ways to drive an agent on the Mac

- **A. Stand-in binary (no model).** Covers 1 and 2. Decided as the first stage.
- **B. A local open model behind the real agent.** Claude Code on the Mac points at an Ollama server with `ANTHROPIC_BASE_URL`; Ollama has a built-in Anthropic-compatible API (docs.ollama.com, Claude Code integration), so no LiteLLM or other translator is needed. The log format and the process tree are the real Claude Code ones. The model can be on the Mac (24 GB: `gpt-oss:20b`, `devstral` and similar, about 14 GB) or on the Windows PC (`gpt-oss:120b` and larger; set `OLLAMA_HOST=0.0.0.0`, `OLLAMA_CONTEXT_LENGTH` at 64k or more, open port 11434 for the LAN). Quality is far below Claude, which does not matter: we need activity, not good code. Open points: whether Claude Code runs on the Mac without reaching Anthropic at all (login, updates, telemetry), and whether the local models handle its long tool prompts. Untried.
- **C. Codex CLI or opencode with a local model** (`codex --oss`, opencode with an OpenAI-compatible endpoint). Same idea; a different log format, so it doubles as a second-agent test. Untried.
- **D. A rented Mac outside China** (EC2 Mac, MacStadium, MacinCloud), screen-shared, running real Claude Code. Real sessions; costs money, needs the device registered for development signing (or SIP/AMFI relaxed), and the system-extension approval is a GUI step. Rent only when the end-to-end run is reached.
- **E. Rejected:** a proxy or VPN to the real APIs (does not last), and running the agent on the Linux box with only the commands on the Mac (the agent's process tree would not be on the Mac).

## Order (proposed)

1. Run the `eslogger` experiment on the Mac (A): real event data, no agent needed.
2. Write the Mac collector requirements from that data.
3. Try B on the Mac with a small local model (in parallel, cheap to try) to see whether Claude Code runs against Ollama there; use the Windows PC if the Mac is too small.
4. Fall back to D for the final real-session acceptance.

## Where the other work stands (so it is not lost)

- **Mac collector:** brief done ([mac-collector.md](mac-collector.md)), requirements in [../mac-collector.md](../mac-collector.md). Decided: Endpoint Security only first (no network), a small Swift extension plus the Go daemon, Unix socket between them, a signed and notarized pkg (not the App Store). Open: whether the entitlement approval covers distribution, the Mac daemon moving to root under launchd, test route (this doc).
- **Codex support:** experiments, requirements ([../multi-agent-support.md](../multi-agent-support.md)) and design ([../multi-agent-design.md](../multi-agent-design.md)) done; no code. Design questions 1 to 3 answered, 4 to 6 open. Paused for the Mac work.
- **Backlog notes this week:** view filters and test-run labelling `8a0af0b`; headless agents and central admin view, comment on `f8cabac` (related `f01190c`, `3c14daa`, `3419102`); Substack `75a7438`.
- **Uncommitted:** `README.md`, `tools/dev/sentient.sh`, this doc, `mac-collector.md`.

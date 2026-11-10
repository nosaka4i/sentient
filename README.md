# sentient
Monitor and track model activity

See [docs/brief.md](docs/brief.md) for the product brief and
[docs/design.md](docs/design.md) for the design.

Two binaries: `sentientd` (the monitor; root, started by systemd) and `sen`
(the CLI; runs as you and asks `sentientd` over a Unix socket).

## Install (v0.0.3)

Manual steps for now; a package or `sen install` can come later.

1. **Tetragon** — *placeholder: arrives with kernel capture (build step 2).*
   Install Tetragon the standard way (its release install) and enable its
   systemd service, so it starts at boot before any agent.

2. **Get the binaries**, either by downloading a release or by building.

   **Download** (Linux amd64 or arm64, macOS on Apple silicon). Pick the
   archive for your machine from the
   [releases page](https://github.com/nosaka4i/sentient/releases), check it
   against `checksums.txt`, and unpack it into a `bin/` and `deploy/` folder
   (the later steps use those paths):

   ```sh
   v=0.0.3; os=linux; arch=amd64        # os: linux or darwin; arch: amd64 or arm64
   base=https://github.com/nosaka4i/sentient/releases/download/v$v
   curl -fsSLO $base/sentient_${v}_${os}_${arch}.tar.gz
   curl -fsSLO $base/checksums.txt
   sha256sum --ignore-missing -c checksums.txt     # on a Mac: shasum -a 256 -c --ignore-missing checksums.txt
   mkdir -p bin deploy && tar -xzf sentient_${v}_${os}_${arch}.tar.gz sen sentientd && mv sen sentientd bin/
   tar -xzf sentient_${v}_${os}_${arch}.tar.gz sentientd.service && mv sentientd.service deploy/
   ```

   Then carry on from step 3 (Linux). The binaries are not signed or
   notarized. Fetching with `curl` as above does not trigger macOS's
   quarantine. If you download in a browser instead, the first run shows
   "Apple could not verify sen is free of malware" (the dialog can sit behind
   the terminal, so the command looks stuck). Click Done, not Move to Trash,
   and clear the flag from the unpacked folder:
   `xattr -dr com.apple.quarantine .`
   (or System Settings, Privacy & Security, Open Anyway, once per binary).

   **On a Mac** there is no kernel collector yet, so sentientd runs in agent
   logs only mode: a view of what the agent says it did, with nothing
   independent confirming it. Skip the Tetragon and systemd steps. Run
   `./sentientd` in one terminal and `./sen serve` in another, then open
   `http://127.0.0.1:7357`. Both use the same default socket under
   `~/Library/Application Support/sentient/`.

   **Or build** (Go 1.26):

   ```sh
   make build            # bin/sen, bin/sentientd; VERSION=x.y.z make build to stamp a version
   ```

3. **Install the binaries and the daemon:**

   ```sh
   sudo install -d -m 0755 /opt/sentient/bin
   sudo install -d -m 0700 /opt/sentient/data
   sudo install -m 0755 bin/sentientd bin/sen /opt/sentient/bin/
   sudo groupadd --system sentient            # who may use the socket
   sudo usermod -aG sentient "$USER"          # log in again to pick it up
   sudo install -m 0644 deploy/sentientd.service /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable --now sentientd
   ```

   By default `sentientd` ingests `~/.claude/projects` of `/root` and every
   home under `/home`; pass `--home <dir>` (repeatable) in the unit to limit
   it.

4. **Put `sen` on the PATH:**

   ```sh
   sudo ln -s /opt/sentient/bin/sen /usr/local/bin/sen
   sen status
   ```

5. **Web dashboard:** `sen serve` serves it on `http://127.0.0.1:7357`
   (loopback only; from another machine use an SSH tunnel:
   `ssh -N -L 7357:127.0.0.1:7357 <host>`). `/health` shows sentient's own
   state.

## Passwordless sudo

sentientd runs as root so the agent can't stop it or edit its records. If
the account the agent runs as can `sudo` without a password, that boundary
is gone, and the /health page warns about it. To fix it, remove `NOPASSWD`
for that account:

```sh
sudo grep -rn NOPASSWD /etc/sudoers /etc/sudoers.d/   # find the rule
sudo visudo -f /etc/sudoers.d/<file>                  # delete NOPASSWD (or the rule)
sudo -l -U <account>                                  # check: no NOPASSWD left
```

Better still, run agents in a sandbox or a separate account without sudo.

## Development

Run `sentientd` unprivileged with paths under `.claudetmp/`:

```sh
go build -o .claudetmp/bin/ ./cmd/...
.claudetmp/bin/sentientd --data-dir .claudetmp/dev/data \
    --socket .claudetmp/dev/sentientd.sock --socket-group "" --home "$HOME"
SENTIENT_SOCKET=.claudetmp/dev/sentientd.sock .claudetmp/bin/sen status
```

`tools/dev/sentient.sh` does the same with the paths filled in (symlink it
as `sentient` on your PATH). One foreground process per terminal; logs go to
`logs/` in the repo. Data, socket and logs stay under the repo, so nothing
touches `/opt/sentient`.

```sh
sentient build       # make build (bin/sen, bin/sentientd)
sentient install     # make install (into $GOBIN)
sentient sentientd   # the daemon, unprivileged, agent logs only (no kernel capture)
sentient kernel      # the daemon as root with --tetragon (SENTIENT_VERBOSITY=essential|extended|complete)
sentient serve       # the web UI on 127.0.0.1:7357 (SENTIENT_ADDR to change); finds the daemon from sentientd or kernel
```

On a Mac use `sentientd` and `serve`; `kernel` needs Tetragon, which is
Linux-only. `serve` passes the same `--socket` as `sentientd`. Running
`bin/sen serve` by hand uses the default socket instead
(`~/Library/Application Support/sentient/sentientd.sock` on a Mac), where
nothing listens, and the page says it cannot reach the monitor. Pass the
same `--socket` to both.

- `make test` / `make cover` — unit tests (keep each package ≥70% covered).
- `make proto` — regenerate `internal/api/sentientpb` from `sentient.proto`
  (generated code is committed; `make tools` installs buf and the protoc
  plugins under `.claudetmp/tools/` first).
- The local API can be poked by hand:
  `grpcurl -plaintext -unix -import-path internal/api/sentientpb -proto sentient.proto .claudetmp/dev/sentientd.sock sentient.v1.Sentient/Status`

# sentient
Monitor and track model activity

See [docs/brief.md](docs/brief.md) for the product brief and
[docs/design.md](docs/design.md) for the design.

Two binaries: `sentientd` (the monitor; root, started by systemd) and `sen`
(the CLI; runs as you and asks `sentientd` over a Unix socket).

## Install (v0.1)

Manual steps for now; a package or `sen install` can come later.

1. **Tetragon** — *placeholder: arrives with kernel capture (build step 2).*
   Install Tetragon the standard way (its release install) and enable its
   systemd service, so it starts at boot before any agent.

2. **Build** (Go 1.26):

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

- `make test` / `make cover` — unit tests (keep each package ≥70% covered).
- `make proto` — regenerate `internal/api/sentientpb` from `sentient.proto`
  (generated code is committed; `make tools` installs buf and the protoc
  plugins under `.claudetmp/tools/` first).
- The local API can be poked by hand:
  `grpcurl -plaintext -unix -import-path internal/api/sentientpb -proto sentient.proto .claudetmp/dev/sentientd.sock sentient.v1.Sentient/Status`

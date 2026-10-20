#!/usr/bin/env bash
# Dev helper, same shape as nosaka's tools/dev/nosaka.sh: one script, one
# foreground process per invocation, tee already baked in so logs land in
# logs/<name>.log (repo-root, gitignored). Run each in its own terminal tab
# or tmux pane; stopping one is just Ctrl-C there.
#
# Usage:
#   ./sentient.sh build       # throttled `make build` (bin/sen, bin/sentientd)
#   ./sentient.sh sentientd   # the monitor daemon, unprivileged dev mode
#   ./sentient.sh serve       # the web UI on 127.0.0.1:7357
#
# Dev data, socket and logs stay under the repo (.claudetmp/dev/, logs/),
# so nothing touches /opt/sentient or /run/sentient. To view the UI from a
# Mac: ssh -N -L 7357:127.0.0.1:7357 nosaka, then http://localhost:7357.
#
# Override with env vars if needed: SENTIENT_ADDR (serve), SENTIENT_DEV_DIR.

set -euo pipefail

SERVICE="${1:-}"

# Symlink-resolving, so it works the same via a symlink (e.g. ~/bin/sentient)
# or directly.
SOURCE="${BASH_SOURCE[0]}"
while [ -h "$SOURCE" ]; do
  DIR="$(cd -P "$(dirname "$SOURCE")" && pwd)"
  SOURCE="$(readlink "$SOURCE")"
  [[ $SOURCE != /* ]] && SOURCE="$DIR/$SOURCE"
done
SCRIPT_DIR="$(cd -P "$(dirname "$SOURCE")" && pwd)"
REPO_ROOT="$(cd -P "$SCRIPT_DIR/../.." && pwd)"

DEV_DIR="${SENTIENT_DEV_DIR:-$REPO_ROOT/.claudetmp/dev}"
DATA_DIR="$DEV_DIR/data"
SOCKET="$DEV_DIR/sentientd.sock"
ADDR="${SENTIENT_ADDR:-127.0.0.1:7357}"
LOG_DIR="$REPO_ROOT/logs"
mkdir -p "$DATA_DIR" "$LOG_DIR"

need_bin() {
  if [ ! -x "$REPO_ROOT/bin/$1" ]; then
    echo "bin/$1 not found — run: $0 build" >&2
    exit 1
  fi
}

case "$SERVICE" in

  build)
    cd "$REPO_ROOT"
    make build 2>&1 | tee -a "$LOG_DIR/build.log"
    ;;

  sentientd)
    need_bin sentientd
    cd "$REPO_ROOT"
    bin/sentientd --data-dir "$DATA_DIR" --socket "$SOCKET" --socket-group "" --home "$HOME" 2>&1 | tee -a "$LOG_DIR/sentientd.log"
    ;;

  serve)
    need_bin sen
    if [ ! -S "$SOCKET" ]; then
      echo "sentientd isn't running (no socket at $SOCKET) — start it first: $0 sentientd" >&2
      exit 1
    fi
    cd "$REPO_ROOT"
    bin/sen serve --addr "$ADDR" --socket "$SOCKET" 2>&1 | tee -a "$LOG_DIR/serve.log"
    ;;

  *)
    echo "usage: $0 {build|sentientd|serve}" >&2
    exit 1
    ;;

esac

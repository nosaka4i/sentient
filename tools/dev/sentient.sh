#!/usr/bin/env bash
# Dev helper, same shape as nosaka's tools/dev/nosaka.sh: one script, one
# foreground process per invocation, tee already baked in so logs land in
# logs/<name>.log (repo-root, gitignored). Run each in its own terminal tab
# or tmux pane; stopping one is just Ctrl-C there.
#
# Usage:
#   ./sentient.sh build       # throttled `make build` (bin/sen, bin/sentientd)
#   ./sentient.sh install     # throttled `make install` (sen, sentientd into $GOBIN / $GOPATH/bin)
#   ./sentient.sh sentientd   # the monitor daemon, unprivileged dev mode
#   ./sentient.sh kernel      # the daemon as root with --tetragon, scratch data in .claudetmp/dev/kdata, logs/sentientd-kernel.log
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

  install)
    cd "$REPO_ROOT"
    make install 2>&1 | tee -a "$LOG_DIR/build.log"
    echo "installed to ${GOBIN:-$(go env GOPATH)/bin}"
    ;;

  sentientd)
    need_bin sentientd
    cd "$REPO_ROOT"
    bin/sentientd --data-dir "$DATA_DIR" --socket "$SOCKET" --socket-group "" --home "$HOME" 2>&1 | tee -a "$LOG_DIR/sentientd.log"
    ;;

  kernel)
    # sentientd as root, reading Tetragon's socket (root-only), into a scratch
    # database so it never touches real data. After Ctrl-C, give yourself the
    # files back to inspect them: sudo chown -R "$USER": $DEV_DIR/kdata
    # The socket gets your group, so `sen serve` (as you) can reach the root daemon.
    need_bin sentientd
    cd "$REPO_ROOT"
    KDATA="${SENTIENT_KERNEL_DIR:-$DEV_DIR/kdata}"
    mkdir -p "$KDATA"
    sudo bin/sentientd --tetragon --home "$HOME" --data-dir "$KDATA" --socket "$KDATA/s.sock" --socket-group "$(id -gn)" 2>&1 | tee -a "$LOG_DIR/sentientd-kernel.log"
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
    echo "usage: $0 {build|install|sentientd|kernel|serve}" >&2
    exit 1
    ;;

esac

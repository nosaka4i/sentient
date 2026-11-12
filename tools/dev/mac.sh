#!/usr/bin/env bash
# Mac dev helper for the Endpoint Security prototype (apps/mac/prototype).
# Runs on the Mac only. One short command per step, so nothing is copied by hand.
# Not tested on a Mac when written: if a step fails, paste its output.
#
# Usage:
#   tools/dev/mac.sh build      # xcodebuild the host app and the extension (Debug)
#   tools/dev/mac.sh install    # copy the newest build to /Applications, open it, show status
#   tools/dev/mac.sh status     # extension version/state, process, capture file size
#   tools/dev/mac.sh reset      # move the current capture aside (sudo) so the next one starts clean
#   tools/dev/mac.sh capture    # scp the capture and the Claude session logs to the Linux box
#
# Remember: bump CURRENT_PROJECT_VERSION in the project (the Linux side does it) or
# macOS keeps the old extension running. `status` prints the running version.
#
# Overrides: SENTIENT_REMOTE (ssh alias, default nosaka), SENTIENT_REMOTE_DIR.

set -euo pipefail

CMD="${1:-}"
SCRIPT_DIR="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -P "$SCRIPT_DIR/../.." && pwd)"
PROJECT_DIR="$REPO_ROOT/apps/mac/prototype/Untitled Project"
PROJECT="$PROJECT_DIR/Untitled Project.xcodeproj"
SCHEME="Untitled Project"
APP_NAME="Untitled Project.app"
EXT_ID="ai.nosaka.sentientes.SentientES"
CAPTURE="/var/tmp/sentient-es.jsonl"
REMOTE="${SENTIENT_REMOTE:-nosaka}"
REMOTE_DIR="${SENTIENT_REMOTE_DIR:-work/src/github.com/nosaka4i/sentient/.claudetmp/es-experiment}"

newest_build() {
  # Newest Debug build of the host app under DerivedData.
  ls -dt "$HOME"/Library/Developer/Xcode/DerivedData/*/Build/Products/Debug/"$APP_NAME" 2>/dev/null | head -1
}

status() {
  echo "== extension (systemextensionsctl)"
  systemextensionsctl list | grep -i sentient || echo "(not installed)"
  echo "== process"
  ps aux | grep -i '[S]entientES' || echo "(not running)"
  echo "== capture file"
  ls -l "$CAPTURE" 2>/dev/null || echo "(no $CAPTURE)"
  if [ -f "$CAPTURE" ]; then
    echo "lines: $(wc -l < "$CAPTURE")   open events: $(grep -c '"type":"open"' "$CAPTURE" || true)"
  fi
}

case "$CMD" in
  build)
    xcodebuild -project "$PROJECT" -scheme "$SCHEME" -configuration Debug -allowProvisioningUpdates build
    ;;
  install)
    SRC="$(newest_build)"
    if [ -z "$SRC" ]; then
      echo "no build found under DerivedData; run: $0 build (or build in Xcode)" >&2
      exit 1
    fi
    echo "installing $SRC"
    rm -rf "/Applications/$APP_NAME"
    cp -R "$SRC" /Applications/
    # A running old copy would just be brought forward by `open`, so quit it first.
    osascript -e "tell application \"Untitled Project\" to quit" >/dev/null 2>&1 || true
    sleep 1
    open "/Applications/$APP_NAME"
    sleep 8
    status
    echo
    echo "If the state is 'waiting for user', approve it in System Settings > General >"
    echo "Login Items & Extensions > Endpoint Security Extensions, and check Full Disk Access."
    ;;
  status)
    status
    ;;
  reset)
    if [ -f "$CAPTURE" ]; then
      sudo mv "$CAPTURE" "$CAPTURE.$(date +%Y%m%d-%H%M%S)"
      echo "moved the old capture aside; the running extension keeps writing to it until it restarts"
    else
      echo "no capture file"
    fi
    ;;
  capture)
    TS="$(date +%Y%m%d-%H%M%S)"
    scp "$CAPTURE" "$REMOTE:$REMOTE_DIR/es-$TS.jsonl"
    scp "$HOME"/.claude/projects/-Users-*-cctest/*.jsonl "$REMOTE:$REMOTE_DIR/"
    echo "sent es-$TS.jsonl and the cctest session logs to $REMOTE:$REMOTE_DIR"
    ;;
  *)
    echo "usage: $0 {build|install|status|reset|capture}" >&2
    exit 2
    ;;
esac

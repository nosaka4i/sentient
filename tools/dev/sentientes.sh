#!/usr/bin/env bash
# Mac helper for the Sentient app and its SentientES extension (apps/mac/Sentient).
# Runs on the Mac only. Running sentientd for the extension is tools/dev/sentient.sh.
#
# Usage:
#   tools/dev/sentientes.sh build      # xcodebuild the app and the extension (Debug)
#   tools/dev/sentientes.sh install    # copy the newest build to /Applications, open it, show status
#   tools/dev/sentientes.sh status     # the extension's state and process
#
# Raise CURRENT_PROJECT_VERSION (Build) in the project on every extension change,
# or macOS keeps the old extension running. `status` shows the running version.

set -euo pipefail

CMD="${1:-}"
SCRIPT_DIR="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -P "$SCRIPT_DIR/../.." && pwd)"
PROJECT="$REPO_ROOT/apps/mac/Sentient/Sentient.xcodeproj"
SCHEME="Sentient"
APP_NAME="Sentient.app"

newest_build() {
  # Newest Debug build of the app under DerivedData.
  ls -dt "$HOME"/Library/Developer/Xcode/DerivedData/Sentient-*/Build/Products/Debug/"$APP_NAME" 2>/dev/null | head -1
}

status() {
  echo "== extension (systemextensionsctl)"
  systemextensionsctl list | grep -i sentient || echo "(not installed)"
  echo "== process"
  ps aux | grep -i '[S]entientES' || echo "(not running)"
}

case "$CMD" in
  build)
    # xcode-select may point at the command line tools; use the Xcode app for this command only.
    if [ -z "${DEVELOPER_DIR:-}" ]; then
      XCODE_APP="$(ls -d /Applications/Xcode*.app 2>/dev/null | sort | tail -1)"
      if [ -n "$XCODE_APP" ]; then
        export DEVELOPER_DIR="$XCODE_APP/Contents/Developer"
      fi
    fi
    echo "using ${DEVELOPER_DIR:-the active developer directory}"
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
    osascript -e "tell application \"Sentient\" to quit" >/dev/null 2>&1 || true
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
  *)
    echo "usage: $0 {build|install|status}" >&2
    exit 2
    ;;
esac

#!/usr/bin/env bash
set -euo pipefail
proof=$(cd -- "$(dirname -- "$0")" && pwd)
repo=${1:-$REPO}
temp=$(mktemp -d $TMP)
trap 'rm -rf -- "$temp"' EXIT
git -C "$repo" archive 75ba671382b0d109f4833b50fd025a76ec672bb7 | tar -x -C "$temp"
cp "$proof/baseline-tests/internal/app/own_message_read_test.go" "$temp/internal/app/"
cp "$proof/baseline-tests/cmd/wacli/send_poll_test.go" "$temp/cmd/wacli/"
if flock $TMP bash -c 'bash $TMP 1400 >/dev/null; cd "$1"; go test -v ./internal/app ./cmd/wacli -run "Test(HistoryStoredOwnReplyPreservesManualUnread|HistoryOwnReplyPreservesUnresolvedSameSecond|LiveTombstonedOwnReplyReplayPreservesUnread|PersistOutboundVotePreservesUnread)$" -count=1' _ "$temp" > "$proof/regressions-before.log" 2>&1; then
  echo 'ERROR: expected the four baseline regressions to fail' >&2
  exit 1
fi
for test in HistoryStoredOwnReplyPreservesManualUnread HistoryOwnReplyPreservesUnresolvedSameSecond LiveTombstonedOwnReplyReplayPreservesUnread PersistOutboundVotePreservesUnread; do
  grep -q -- "^--- FAIL: Test$test " "$proof/regressions-before.log"
done
echo 'PASS: all four regressions fail on exact 75ba671, on unread-state assertions'

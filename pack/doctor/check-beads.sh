#!/bin/sh
# kourt doctor: is the beads /v0 door up, and is the events journal on?
# Without the journal, timelines are reconstructed lossily from each bead's
# createRev/modRev (kourt says so in every verdict).
set -eu

BEADS="${KOURT_BEADS:-http://127.0.0.1:8099}"
if ! command -v curl >/dev/null 2>&1; then
  echo "curl not found; cannot probe $BEADS"
  exit 2
fi
if ! curl -fsS -m 5 "$BEADS/v0/beads/context" >/dev/null 2>&1; then
  echo "beads /v0 unreachable at $BEADS"
  echo "Start it: bd -C <workspace> serve --addr 127.0.0.1:8099  (or set KOURT_BEADS)"
  exit 2
fi
code=$(curl -s -o /dev/null -w '%{http_code}' -m 5 "$BEADS/v0/beads/events?since=0&limit=1")
case "$code" in
  200) echo "beads /v0 reachable at $BEADS; events journal ON (exact timelines)" ;;
  409) echo "beads /v0 reachable at $BEADS; events journal OFF -> timelines are LOSSY"
       echo "Enable: bd config set events-journal true (every writer), then restart kine-beads and bd serve"
       exit 1 ;;
  *)   echo "beads /v0 reachable at $BEADS; events endpoint answered HTTP $code"
       exit 1 ;;
esac
exit 0

#!/usr/bin/env bash
# ci/down.sh — tear down everything ci/up.sh started. Safe to run twice; it
# never fails the job (the workflow runs it with `if: always()` and the test
# step's exit code is what decides red/green).
set -uo pipefail
WORKDIR="${KOURT_CI_DIR:-$HOME/k3s-beads}"
log() { echo "[down] $*" >&2; }

if command -v k3s-uninstall.sh >/dev/null 2>&1 || [ -x /usr/local/bin/k3s-uninstall.sh ]; then
  log "uninstalling k3s"
  sudo /usr/local/bin/k3s-uninstall.sh >/dev/null 2>&1 || true
fi
for p in bd kine dolt; do
  f="$WORKDIR/$p.pid"
  if [ -f "$f" ]; then
    pid=$(cat "$f")
    log "stopping $p (pid $pid)"
    kill "$pid" >/dev/null 2>&1 || true
    for _ in $(seq 1 20); do kill -0 "$pid" >/dev/null 2>&1 || break; sleep 0.5; done
    kill -9 "$pid" >/dev/null 2>&1 || true
    rm -f "$f"
  fi
done
# Keep logs for the artifact upload step; drop the data.
rm -rf "$WORKDIR/.beads" "$WORKDIR/kine-src"
log "down"
exit 0

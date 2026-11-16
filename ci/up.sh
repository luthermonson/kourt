#!/usr/bin/env bash
# ci/up.sh — boot k3s-on-beads on a Linux runner.
#
# SKELETON. Every step is real but the whole has not been run on a GitHub
# runner yet. Produces, and exports via $GITHUB_ENV when present:
#   KUBECONFIG      kubeconfig for the k3s apiserver
#   KOURT_BEADS  the beads /v0 base URL (bd serve)
#
# Topology (mirrors the working macOS setup, 2026-11-15):
#   dolt sql-server :3307  <- kine-beads (beads library, server mode) :2379 <- k3s apiserver
#                          <- bd serve :8099 (same workspace, read by kourt)
#
# Open question (see README "CI"): kine-beads is built from the fork branch
# `beads-backend` of github.com/luthermonson/kine with the beads library
# compiled in. On macOS that needed CGO_ENABLED=1 and -tags gms_pure_go
# because the beads library links embedded Dolt. In SERVER mode the embedded
# engine is never opened, but it is still linked; whether a CGO_ENABLED=0
# build is possible depends on beads' build tags, which we have not verified.
set -euo pipefail

WORKDIR="${KOURT_CI_DIR:-$HOME/k3s-beads}"
DOLT_VERSION="${DOLT_VERSION:-2.6.0}"
BD_VERSION="${BD_VERSION:-1.3.4}"
K3S_VERSION="${K3S_VERSION:-v1.37.2+k3s1}"
KINE_REPO="${KINE_REPO:-https://github.com/luthermonson/kine.git}"
KINE_BRANCH="${KINE_BRANCH:-beads-backend}"
DOLT_PORT=3307
KINE_PORT=2379
BEADS_PORT=8099

log() { echo "[up] $*" >&2; }
mkdir -p "$WORKDIR/.beads" "$WORKDIR/bin" "$WORKDIR/log"
export PATH="$WORKDIR/bin:$PATH"

# 1. dolt
if ! command -v dolt >/dev/null 2>&1; then
  log "installing dolt $DOLT_VERSION"
  curl -fsSL "https://github.com/dolthub/dolt/releases/download/v${DOLT_VERSION}/dolt-linux-amd64.tar.gz" \
    | tar -xz -C "$WORKDIR" && mv "$WORKDIR/dolt-linux-amd64/bin/dolt" "$WORKDIR/bin/dolt"
fi
dolt config --global --add user.email ci@kourt.invalid >/dev/null
dolt config --global --add user.name kourt-ci >/dev/null

# 2. bd (beads CLI; provides `bd serve` and the workspace init)
if ! command -v bd >/dev/null 2>&1; then
  log "installing bd $BD_VERSION"
  curl -fsSL "https://github.com/gastownhall/beads/releases/download/v${BD_VERSION}/beads_${BD_VERSION}_linux_amd64.tar.gz" \
    | tar -xz -C "$WORKDIR/bin" bd
fi

# 3. the beads workspace (server mode) + dolt sql-server
#    metadata.json tells every beads client (kine-beads, bd serve) to talk to
#    the sql-server instead of opening embedded Dolt.
cat > "$WORKDIR/.beads/metadata.json" <<EOF
{"backend":"dolt","dolt_mode":"server","dolt_server_host":"127.0.0.1","dolt_server_port":${DOLT_PORT}}
EOF
mkdir -p "$WORKDIR/.beads/dolt"
(cd "$WORKDIR/.beads/dolt" && [ -d beads ] || { mkdir beads && cd beads && dolt init >/dev/null; })
log "starting dolt sql-server on :$DOLT_PORT"
nohup dolt sql-server --host 127.0.0.1 --port "$DOLT_PORT" --data-dir "$WORKDIR/.beads/dolt" \
  > "$WORKDIR/log/dolt.log" 2>&1 &
echo $! > "$WORKDIR/dolt.pid"
for _ in $(seq 1 30); do dolt sql-client -H 127.0.0.1 -P "$DOLT_PORT" -u root -q "select 1" >/dev/null 2>&1 && break; sleep 1; done

# The events journal must be ON for every WRITER before kine starts, or the
# timeline is reconstructed lossily from cr/mr. (bd 1.3.4: config.yaml key.)
printf 'events-journal: true\nevents-journal-retain-days: 1\n' > "$WORKDIR/.beads/config.yaml"
export BD_EVENTS_JOURNAL=1

# 4. kine-beads (server mode)
if [ ! -x "$WORKDIR/bin/kine-beads" ]; then
  log "building kine-beads from $KINE_REPO@$KINE_BRANCH"
  git clone --depth 1 --branch "$KINE_BRANCH" "$KINE_REPO" "$WORKDIR/kine-src"
  # See the CGO note at the top of this file.
  (cd "$WORKDIR/kine-src" && CGO_ENABLED=1 go build -tags gms_pure_go -o "$WORKDIR/bin/kine-beads" .)
fi
log "starting kine-beads on :$KINE_PORT"
nohup "$WORKDIR/bin/kine-beads" \
  --endpoint "beads://${WORKDIR}?server=127.0.0.1:${DOLT_PORT}" \
  --listen-address "tcp://0.0.0.0:${KINE_PORT}" \
  > "$WORKDIR/log/kine.log" 2>&1 &
echo $! > "$WORKDIR/kine.pid"
sleep 3

# 5. bd serve — the /v0 door kourt reads
log "starting bd serve on :$BEADS_PORT"
nohup bd -C "$WORKDIR" serve --addr "127.0.0.1:${BEADS_PORT}" > "$WORKDIR/log/bd-serve.log" 2>&1 &
echo $! > "$WORKDIR/bd.pid"
for _ in $(seq 1 30); do curl -fsS "http://127.0.0.1:${BEADS_PORT}/v0/beads/context" >/dev/null 2>&1 && break; sleep 1; done

# 6. k3s pointed at kine
log "installing k3s $K3S_VERSION with --datastore-endpoint http://127.0.0.1:${KINE_PORT}"
curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION="$K3S_VERSION" INSTALL_K3S_EXEC="server \
  --datastore-endpoint=http://127.0.0.1:${KINE_PORT} \
  --write-kubeconfig-mode=644 \
  --disable=traefik,servicelb,metrics-server" sh -s -
for _ in $(seq 1 60); do sudo k3s kubectl get nodes >/dev/null 2>&1 && break; sleep 2; done
cp /etc/rancher/k3s/k3s.yaml "$WORKDIR/kubeconfig"

export KUBECONFIG="$WORKDIR/kubeconfig"
export KOURT_BEADS="http://127.0.0.1:${BEADS_PORT}"
if [ -n "${GITHUB_ENV:-}" ]; then
  { echo "KUBECONFIG=$KUBECONFIG"; echo "KOURT_BEADS=$KOURT_BEADS"; echo "KOURT_CI_DIR=$WORKDIR"; } >> "$GITHUB_ENV"
fi
log "up: KUBECONFIG=$KUBECONFIG KOURT_BEADS=$KOURT_BEADS"
kubectl get nodes
curl -fsS "$KOURT_BEADS/v0/beads/issues:count"; echo

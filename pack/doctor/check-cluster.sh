#!/bin/sh
# litecone doctor: can we reach the k3s-on-beads apiserver that plans apply to?
set -eu

if [ -z "${KUBECONFIG:-}" ] && [ ! -f "$HOME/.kube/config" ]; then
  echo "no kubeconfig: set KUBECONFIG to the k3s-on-beads cluster (e.g. ~/k3s-beads/kubeconfig)"
  exit 2
fi
if ! command -v litecone >/dev/null 2>&1; then
  echo "litecone binary not on PATH"
  echo "Build it (make build) or install a release; plans are applied through it, not kubectl."
  exit 2
fi
if command -v kubectl >/dev/null 2>&1; then
  if ! kubectl get --raw=/readyz --request-timeout=10s >/dev/null 2>&1; then
    echo "apiserver not ready via kubectl (KUBECONFIG=${KUBECONFIG:-~/.kube/config})"
    exit 2
  fi
  echo "cluster reachable: $(kubectl get nodes --no-headers 2>/dev/null | wc -l | tr -d ' ') node(s)"
else
  echo "cluster: kubectl not installed; litecone uses client-go directly, skipping the readyz probe"
fi
exit 0

#!/usr/bin/env bash

# set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
KUBE_DIR="${REPO_ROOT}/.kube"
CLUSTER_NAME="${CLUSTER_NAME:-kcp}"
KCP_NAMESPACE="${KCP_NAMESPACE:-kcp}"
OPERATOR_NAMESPACE="${OPERATOR_NAMESPACE:-kcp-operator}"
DEPLOYMENT_TIMEOUT="${DEPLOYMENT_TIMEOUT:-600s}"
export KIND_EXPERIMENTAL_PROVIDER="${KIND_EXPERIMENTAL_PROVIDER:-podman}"

kubeconfig_path() {
  case "$1" in
    /*) printf '%s\n' "$1" ;;
    *) printf '%s/%s\n' "${KUBE_DIR}" "$1" ;;
  esac
}

KIND_CONFIG="$(kubeconfig_path "${KIND_CONFIG:-kind.kubeconfig}")"
ADMIN_KUBECONFIG="$(kubeconfig_path "${ADMIN_KUBECONFIG:-admin.kubeconfig}")"
KCP_KUBECONFIG="/tmp/${KCP_KUBECONFIG:-kcp-admin.kubeconfig}"

log() {
  echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] $*" >&2
}

require_commands() {
  local cmd
  for cmd in "$@"; do
    if ! command -v "${cmd}" >/dev/null 2>&1; then
      log "ERROR: required command '${cmd}' not found on PATH."
      return 1
    fi
  done
}

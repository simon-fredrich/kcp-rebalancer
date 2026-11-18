#!/usr/bin/env bash
#
# Enables LogicalClusterMigration on the root and every shard, then binds the
# migration API in root. Uses a temporary port-forward for the root API request.
#
# Usage: hack/enable-migrations.sh
# Requires the hosting and wildcard kubeconfigs under .kube (or configured paths).

set -euo pipefail
umask 077

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_commands kubectl jq
for config in "${KIND_CONFIG}" "${KCP_KUBECONFIG}"; do
  if [[ ! -f "${config}" ]]; then
    log "ERROR: kubeconfig not found: ${config}"
    exit 1
  fi
done

TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/kcp-migrations.XXXXXX")"
FORWARD_PID=""
FORWARD_PORT=""
cleanup() {
  if [[ -n "${FORWARD_PID}" ]]; then
    if kill -0 "${FORWARD_PID}" 2>/dev/null; then
      kill "${FORWARD_PID}"
    fi
    wait "${FORWARD_PID}" || true
  fi
  rm -f "${TEMP_DIR}/forward.log"
  rmdir "${TEMP_DIR}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

enable_feature_gate() {
  local resource="$1"
  local name="$2"
  local patch

  patch="$(kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" \
    get "${resource}" "${name}" -o json |
    jq -c '
      (.spec.extraArgs // []) as $args
      | reduce range(0; ($args | length)) as $i
          ({args: [], gates: []};
            if $args[$i] == "--feature-gates" then
              .gates += [($args[$i + 1] // "")]
            elif ($args[$i] | startswith("--feature-gates=")) then
              .gates += [($args[$i] | ltrimstr("--feature-gates="))]
            elif $i > 0 and $args[$i - 1] == "--feature-gates" then
              .
            else
              .args += [$args[$i]]
            end)
      | . as $parsed
      | ($parsed.gates
          | map(select(length > 0))
          | join(",")
          | split(",")
          | (map(select(startswith("LogicalClusterMigration=") | not))
             + ["LogicalClusterMigration=true"])
          | join(",")) as $gates
      | {spec: {extraArgs: ($parsed.args + ["--feature-gates=" + $gates])}}
    ')"

  log "Enabling LogicalClusterMigration on ${resource}/${name}."
  kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" \
    patch "${resource}" "${name}" --type=merge -p "${patch}"
}

enable_feature_gate rootshard root
shards="$(kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" \
  get shards -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')"
if [[ -z "${shards}" ]]; then
  log "ERROR: no shards found in namespace ${KCP_NAMESPACE}."
  exit 1
fi
while IFS= read -r shard; do
  [[ -n "${shard}" ]] || continue
  enable_feature_gate shard "${shard}"
done <<< "${shards}"

kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" wait \
  --for=condition=Available rootshard/root --timeout="${DEPLOYMENT_TIMEOUT}"
while IFS= read -r shard; do
  [[ -n "${shard}" ]] || continue
  kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" wait \
    --for=condition=Available "shard/${shard}" --timeout="${DEPLOYMENT_TIMEOUT}"
done <<< "${shards}"

kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" \
  port-forward --address=127.0.0.1 svc/root-kcp :6443 \
  > "${TEMP_DIR}/forward.log" 2>&1 &
FORWARD_PID="$!"
for ((attempt = 0; attempt < 100; attempt++)); do
  FORWARD_PORT="$(sed -n 's/^Forwarding from 127\.0\.0\.1:\([0-9]*\) -> 6443$/\1/p' \
    "${TEMP_DIR}/forward.log")"
  if [[ -n "${FORWARD_PORT}" ]]; then
    break
  fi
  if ! kill -0 "${FORWARD_PID}" 2>/dev/null; then
    break
  fi
  sleep 0.2
done
if [[ -z "${FORWARD_PORT}" ]]; then
  log "ERROR: root-shard port-forward failed to become ready."
  cat "${TEMP_DIR}/forward.log" >&2
  exit 1
fi

kcp_kubectl() {
  kubectl --kubeconfig="${KCP_KUBECONFIG}" \
    --server="https://127.0.0.1:${FORWARD_PORT}/clusters/root" \
    --tls-server-name=localhost "$@"
}

log "Binding migration.kcp.io in root."
kcp_kubectl apply -f - <<'EOF'
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: migration
spec:
  reference:
    export:
      path: root
      name: migration.kcp.io
EOF
kcp_kubectl wait --for=jsonpath='{.status.phase}'=Bound apibinding/migration \
  --timeout="${DEPLOYMENT_TIMEOUT}"

log "Logical cluster migration is enabled on root and all shards, and the API binding is Bound."
#!/usr/bin/env bash

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"

main() {
  require_commands helm kubectl
  log "Deleting KCP resources from namespace '${KCP_NAMESPACE}'."
  # Include dynamically added shards and their etcd instances, not just the initial two.
  local resource
  for resource in kubeconfigs.operator.kcp.io frontproxies.operator.kcp.io \
    shards.operator.kcp.io rootshards.operator.kcp.io \
    certificates.cert-manager.io issuers.cert-manager.io etcds.druid.gardener.cloud; do
    kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" delete "${resource}" \
      --all --ignore-not-found --wait=true --timeout="${DEPLOYMENT_TIMEOUT}"
  done
  kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" delete service/front-proxy-nodeport \
    --ignore-not-found --wait=true --timeout="${DEPLOYMENT_TIMEOUT}"
  helm --kubeconfig="${KIND_CONFIG}" uninstall etcd-druid --namespace "${KCP_NAMESPACE}" \
    --ignore-not-found --wait --timeout "${DEPLOYMENT_TIMEOUT}"
  helm --kubeconfig="${KIND_CONFIG}" uninstall kcp-operator --namespace "${OPERATOR_NAMESPACE}" \
    --ignore-not-found --wait --timeout "${DEPLOYMENT_TIMEOUT}"
  rm -f "${ADMIN_KUBECONFIG}" "${KCP_KUBECONFIG}"
  log "KCP teardown complete; namespaces and storage volumes are retained."
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi

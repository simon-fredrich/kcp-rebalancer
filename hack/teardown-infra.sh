#!/usr/bin/env bash

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"

main() {
  require_commands kind
  log "Deleting kind cluster '${CLUSTER_NAME}'..."
  kind delete cluster --name "${CLUSTER_NAME}"
  log "Removing generated kubeconfigs."
  rm -f "${KIND_CONFIG}" "${ADMIN_KUBECONFIG}" "${KCP_KUBECONFIG}"
  log "Infrastructure teardown complete."
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi

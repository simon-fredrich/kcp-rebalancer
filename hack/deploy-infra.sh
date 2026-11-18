#!/usr/bin/env bash

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"

CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.21.2}"

preflight_checks() {
  log "Running pre-flight checks..."
  require_commands "${KIND_EXPERIMENTAL_PROVIDER}" kind kubectl

  if ! "${KIND_EXPERIMENTAL_PROVIDER}" info &>/dev/null; then
    log "ERROR: ${KIND_EXPERIMENTAL_PROVIDER} is not running."
    exit 1
  fi

  log "Pre-flight checks passed."
}

create_kind_cluster() {
  mkdir -p "$(dirname "${KIND_CONFIG}")"

  local clusters
  clusters="$(kind get clusters)"
  if grep -Fxq -- "${CLUSTER_NAME}" <<< "${clusters}"; then
    log "Kind cluster '${CLUSTER_NAME}' already exists — skipping creation."
  else
    log "Creating kind cluster..."

    cat <<EOF | kind create cluster --name "${CLUSTER_NAME}" --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 30443
    hostPort: 8443
    protocol: TCP
EOF
    log "Kind cluster '${CLUSTER_NAME}' created."
  fi
  kind get kubeconfig --name "${CLUSTER_NAME}" > "${KIND_CONFIG}"
  kubectl --kubeconfig="${KIND_CONFIG}" config use-context "kind-${CLUSTER_NAME}"
}

install_cert_manager() {
  kubectl --kubeconfig="${KIND_CONFIG}" apply -f \
    "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"
  kubectl --kubeconfig="${KIND_CONFIG}" -n cert-manager rollout status deployment/cert-manager \
    --timeout="${DEPLOYMENT_TIMEOUT}"
  kubectl --kubeconfig="${KIND_CONFIG}" -n cert-manager rollout status deployment/cert-manager-cainjector \
    --timeout="${DEPLOYMENT_TIMEOUT}"
  kubectl --kubeconfig="${KIND_CONFIG}" -n cert-manager rollout status deployment/cert-manager-webhook \
    --timeout="${DEPLOYMENT_TIMEOUT}"
  log "cert-manager is Available."
}

main() {
  log "=========================================="
  log "  Deploy Infrastructure to Kind Cluster"
  log "=========================================="
  log "Cluster name: ${CLUSTER_NAME}"
  log "Cluster config: ${KIND_CONFIG}"
  log "Deployment Timeout: ${DEPLOYMENT_TIMEOUT}"
  log ""

  preflight_checks
  create_kind_cluster
  install_cert_manager

  log ""
  log "=========================================="
  log "  Infrastructure deployment complete!"
  log "=========================================="
  log "Cluster: ${CLUSTER_NAME}"
  log "cert-manager version: ${CERT_MANAGER_VERSION}"
  log "To tear down: make teardown-infra"
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
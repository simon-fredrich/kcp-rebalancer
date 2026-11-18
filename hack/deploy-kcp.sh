#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
KCP_VERSION="${KCP_VERSION:-v0.33.1}"
ETCD_DRUID_VERSION="${ETCD_DRUID_VERSION:-v0.39.0}"
ETCD_IMAGE="${ETCD_IMAGE:-europe-docker.pkg.dev/gardener-project/public/gardener/etcd-wrapper:v0.6.2}"
ETCD_BACKUP_IMAGE="${ETCD_BACKUP_IMAGE:-europe-docker.pkg.dev/gardener-project/public/gardener/etcdbrctl:v0.41.2}"

preflight_checks() {
  log "Running kcp deployment pre-flight checks..."
  require_commands helm kubectl base64
  log "Pre-flight checks passed."
}

apply_etcd() {
  local etcd_name="$1"
  local namespace="${2:-${KCP_NAMESPACE}}"
  local dry_run="${3:-none}"

  kubectl --kubeconfig="${KIND_CONFIG}" -n "${namespace}" apply --dry-run="${dry_run}" -f - <<EOF
apiVersion: druid.gardener.cloud/v1alpha1
kind: Etcd
metadata:
  name: ${etcd_name}
  labels:
    app: etcd-statefulset
    gardener.cloud/role: controlplane
    role: test
spec:
  annotations:
    app: etcd-statefulset
    gardener.cloud/role: controlplane
    role: test
  labels:
    app: etcd-statefulset
    gardener.cloud/role: controlplane
    role: test
  etcd:
    image: ${ETCD_IMAGE}
    metrics: basic
    defragmentationSchedule: "0 */24 * * *"
    resources:
      limits: { cpu: 500m, memory: 1Gi }
      requests: { cpu: 100m, memory: 200Mi }
    clientPort: 2379
    serverPort: 2380
    quota: 8Gi
  backup:
    image: ${ETCD_BACKUP_IMAGE}
    port: 8080
    fullSnapshotSchedule: "0 */24 * * *"
    resources:
      limits: { cpu: 200m, memory: 1Gi }
      requests: { cpu: 23m, memory: 128Mi }
    garbageCollectionPolicy: Exponential
    garbageCollectionPeriod: 43200s
    deltaSnapshotPeriod: 300s
    deltaSnapshotMemoryLimit: 1Gi
    compression:
      enabled: true
      policy: "gzip"
    leaderElection:
      reelectionPeriod: 5s
      etcdConnectionTimeout: 5s
  sharedConfig:
    autoCompactionMode: periodic
    autoCompactionRetention: "15m"
  replicas: 1
EOF
}

apply_shard() {
  local shard_name="$1"
  local etcd_name="$2"

  kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" apply -f - <<EOF
apiVersion: operator.kcp.io/v1alpha1
kind: Shard
metadata:
  labels:
    app.kubernetes.io/name: kcp-operator
    app.kubernetes.io/managed-by: kustomize
  name: ${shard_name}
spec:
  replicas: 1
  etcd:
    endpoints:
      - http://${etcd_name}-client.${KCP_NAMESPACE}.svc.cluster.local:2379
  rootShard:
    ref:
      name: root
  auth:
    serviceAccount:
      enabled: true
  extraArgs:
    - --feature-gates=LogicalClusterMigration=true
  image:
    tag: ${KCP_VERSION}
EOF
  kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" wait --for=condition=Available "shard/${shard_name}" \
    --timeout="${DEPLOYMENT_TIMEOUT}"
}

deploy_kcp() {
  kubectl --kubeconfig="${KIND_CONFIG}" create namespace "${KCP_NAMESPACE}" --dry-run=client -o yaml |
    kubectl --kubeconfig="${KIND_CONFIG}" apply -f -

  log "Add kcp repo to helm"
  helm --kubeconfig="${KIND_CONFIG}" repo add kcp https://kcp-dev.github.io/helm-charts >/dev/null
  helm --kubeconfig="${KIND_CONFIG}" repo update >/dev/null

  log "Installing or updating kcp-operator in ${OPERATOR_NAMESPACE}."
  helm --kubeconfig="${KIND_CONFIG}" upgrade --install --create-namespace \
    --namespace "${OPERATOR_NAMESPACE}" kcp-operator kcp/kcp-operator \
    --wait --timeout "${DEPLOYMENT_TIMEOUT}"

  log "Installing or updating etcd-druid ${ETCD_DRUID_VERSION} in ${KCP_NAMESPACE}."
  helm --kubeconfig="${KIND_CONFIG}" upgrade --install etcd-druid \
    oci://europe-docker.pkg.dev/gardener-project/releases/charts/gardener/etcd-druid \
    --version "${ETCD_DRUID_VERSION}" \
    --namespace "${KCP_NAMESPACE}" --wait --timeout "${DEPLOYMENT_TIMEOUT}"

  for etcd_name in etcd-root etcd-1 etcd-2; do
    apply_etcd "${etcd_name}"
    kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" wait --for=condition=Ready "etcd/${etcd_name}" \
      --timeout="${DEPLOYMENT_TIMEOUT}"
  done

  log "Create Root Shard"
  kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" apply -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: selfsigned
spec:
  selfSigned: {}
---
apiVersion: operator.kcp.io/v1alpha1
kind: RootShard
metadata:
  name: root
spec:
  external:
    hostname: localhost
    port: 8443
    privateHostname: frontproxy-front-proxy.${KCP_NAMESPACE}.svc.cluster.local
    privatePort: 8443
  certificates:
    issuerRef:
      group: cert-manager.io
      kind: Issuer
      name: selfsigned
  cache:
    embedded:
      enabled: true
  etcd:
    endpoints:
      - http://etcd-root-client.${KCP_NAMESPACE}.svc.cluster.local:2379
  image:
    tag: ${KCP_VERSION}
  extraArgs:
    - --feature-gates=LogicalClusterMigration=true
  replicas: 1
  proxy:
    replicas: 1
    image:
      tag: ${KCP_VERSION}
EOF
  kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" wait --for=condition=Available rootshard/root \
    --timeout="${DEPLOYMENT_TIMEOUT}"

  log "Create Shards"
  apply_shard shard-01 etcd-1
  apply_shard shard-02 etcd-2

  log "Set up Front Proxy"
  kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" apply -f - <<EOF
apiVersion: operator.kcp.io/v1alpha1
kind: FrontProxy
metadata:
  name: frontproxy
spec:
  replicas: 1
  auth:
    serviceAccount:
      enabled: true
  rootShard:
    ref:
      name: root
  serviceTemplate:
    spec:
      type: ClusterIP
      clusterIP: 10.96.100.100
  image:
    tag: ${KCP_VERSION}
  certificateTemplates:
    server:
      spec:
        dnsNames:
          - localhost
        ipAddresses:
          - 127.0.0.1
---
# Operator forces its Service to ClusterIP, kind reaches the frontproxy via this NodePort.
apiVersion: v1
kind: Service
metadata:
  name: front-proxy-nodeport
spec:
  type: NodePort
  selector:
    app.kubernetes.io/component: front-proxy
    app.kubernetes.io/instance: frontproxy
  ports:
    - name: https
      port: 6443
      targetPort: 6443
      nodePort: 30443

EOF
  kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" wait --for=condition=Available frontproxy/frontproxy \
    --timeout="${DEPLOYMENT_TIMEOUT}"

  log "Create admin-kubeconfig"
  kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" apply -f - <<EOF
apiVersion: operator.kcp.io/v1alpha1
kind: Kubeconfig
metadata:
  name: kubeconfig-kcp-admin
spec:
  username: kcp-admin
  groups:
    - system:kcp:admin
  validity: 8766h
  secretRef:
    name: admin-kubeconfig
  target:
    frontProxyRef:
      name: frontproxy
EOF
  kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" wait --for=condition=Available kubeconfig/kubeconfig-kcp-admin \
    --timeout="${DEPLOYMENT_TIMEOUT}"

  local admin_kubeconfig_tmp
  mkdir -p "$(dirname "${ADMIN_KUBECONFIG}")"
  admin_kubeconfig_tmp="$(mktemp "$(dirname "${ADMIN_KUBECONFIG}")/.admin.kubeconfig.XXXXXX")"
  if ! kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" get secret admin-kubeconfig -o jsonpath="{.data.kubeconfig}" | base64 -d > "${admin_kubeconfig_tmp}"; then
    rm -f "${admin_kubeconfig_tmp}"
    return 1
  fi
  if [[ ! -s "${admin_kubeconfig_tmp}" ]]; then
    log "ERROR: admin-kubeconfig Secret contains no kubeconfig."
    rm -f "${admin_kubeconfig_tmp}"
    return 1
  fi
  local admin_cluster
  if ! admin_cluster="$(kubectl --kubeconfig="${admin_kubeconfig_tmp}" config view --minify -o jsonpath='{.clusters[0].name}')" ||
    ! kubectl --kubeconfig="${admin_kubeconfig_tmp}" config set-cluster "${admin_cluster}" \
      --server="https://localhost:8443/clusters/root"; then
    rm -f "${admin_kubeconfig_tmp}"
    return 1
  fi
  mv "${admin_kubeconfig_tmp}" "${ADMIN_KUBECONFIG}"

  kubectl --kubeconfig="${KIND_CONFIG}" -n "${KCP_NAMESPACE}" get pods
  log "kcp deployed."
}

main() {
  log "=========================================="
  log "  Deploy kcp"
  log "=========================================="
  log "Deployment timeout: ${DEPLOYMENT_TIMEOUT}"
  log ""

  preflight_checks
  cd "${REPO_ROOT}"
  deploy_kcp
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
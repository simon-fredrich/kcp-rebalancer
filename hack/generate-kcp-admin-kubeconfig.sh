#!/usr/bin/env bash
#
# Generate a certificate-authenticated admin kubeconfig for the kcp ingress.
# Run with a Kubernetes context for the cluster hosting kcp and cert-manager.
# The ingress must pass TLS through to kcp and trust the kcp-client-ca issuer.
# A TLS-terminating proxy cannot forward client certificates.
# Server TLS uses the system trust store, not kcp's internal server CA.
#
# Usage:
#   bash generate-kcp-admin-kubeconfig.sh [options]
#   -n  kcp namespace (default: kcp)
#   -s  ingress HTTPS URL (default: discovered from ingress/kcp-front-proxy)
#       /clusters/root is appended when no logical-cluster path is provided.
#   -o  output kubeconfig (default: /tmp/kcp-admin.kubeconfig)
#   -i  skip server TLS verification (use only with a trusted local port-forward)
#   -p  discover all shard services, forward locally from 8443, and wait for Ctrl+C
#   -h  show help
#
# Requires: kubectl, jq, openssl, base64.
# The client has system:masters privileges, including wildcard shard access.

set -euo pipefail

NAMESPACE="kcp"
SERVER_URL=""
INSECURE=false
PORT_FORWARD=false
forward_pids=()
forward_services=()
forward_ports=()
temp_dir=""
config_file=""
monitor_pid=""

cleanup() {
  local pid
  for pid in ${forward_pids[@]+"${forward_pids[@]}"} ${monitor_pid:+"$monitor_pid"}; do
    kill "$pid" 2>/dev/null || true
  done
  for pid in ${forward_pids[@]+"${forward_pids[@]}"} ${monitor_pid:+"$monitor_pid"}; do
    wait "$pid" 2>/dev/null || true
  done
  [[ -z "$config_file" ]] || rm -f "$config_file"
  [[ -z "$temp_dir" ]] || rm -rf "$temp_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
OUT_KUBECONFIG="/tmp/kcp-admin.kubeconfig"
CERT_NAME="kcp-apibinding-lifecycle-admin"
SECRET_NAME="kcp-apibinding-lifecycle-admin"
CLIENT_ISSUER="root-client-ca"
CONTEXT_NAME="kcp-apibinding-lifecycle-admin"

while getopts "n:s:o:iph" opt; do
  case "$opt" in
    n) NAMESPACE="$OPTARG" ;;
    s) SERVER_URL="$OPTARG" ;;
    o) OUT_KUBECONFIG="$OPTARG" ;;
    i) INSECURE=true ;;
    p) PORT_FORWARD=true ;;
    h)
      sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) exit 1 ;;
  esac
done
shift "$((OPTIND - 1))"
if [[ $# -ne 0 ]]; then
  printf 'ERROR: unexpected argument: %s\n' "$1" >&2
  exit 1
fi

for tool in kubectl jq openssl base64; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    printf 'ERROR: required command not found: %s\n' "$tool" >&2
    exit 1
  fi
done

if [[ "$PORT_FORWARD" == true ]]; then
  if [[ -n "$SERVER_URL" ]]; then
    printf 'ERROR: -p cannot be combined with -s\n' >&2
    exit 1
  fi
  services=$(kubectl -n "$NAMESPACE" get services -l app.kubernetes.io/name=kcp -o json)
  forward_plan=$(jq -er '
    [.items[]
      | .metadata.labels["app.kubernetes.io/component"] as $component
      | select($component == "rootshard" or $component == "server" or $component == "shard")
      | select(any(.spec.ports[]; .port == 6443 and (.protocol // "TCP") == "TCP"))
      | {name: .metadata.name, root: ($component != "shard")}]
    | sort_by((if .root then 0 else 1 end), .name)
    | if (map(select(.root)) | length) != 1 then
        error("expected exactly one root shard service exposing port 6443")
      else .[].name end
  ' <<< "$services")
  # Match the IPv4 bind address; localhost may resolve to an IPv6 front proxy.
  SERVER_URL="https://127.0.0.1:8443"
  INSECURE=true
fi

if [[ -z "$SERVER_URL" ]]; then
  host=$(kubectl -n "$NAMESPACE" get ingress kcp-front-proxy \
    -o jsonpath='{.spec.rules[0].host}')
  if [[ -z "$host" ]]; then
    printf 'ERROR: kcp-front-proxy ingress has no host; provide -s https://<ingress-host>\n' >&2
    exit 1
  fi
  SERVER_URL="https://$host"
fi
if [[ "$SERVER_URL" != https://?* ]]; then
  printf 'ERROR: -s must be an HTTPS ingress URL\n' >&2
  exit 1
fi
SERVER_URL=${SERVER_URL%/}
if [[ "$SERVER_URL" != */clusters/* ]]; then
  SERVER_URL="$SERVER_URL/clusters/root"
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cert_manifest="$script_dir/kcp-admin-cert.yaml"

echo "1) Applying the admin Certificate (issuer=$CLIENT_ISSUER)" >&2
kubectl create --dry-run=client -f "$cert_manifest" -o json \
  | jq --arg namespace "$NAMESPACE" '.metadata.namespace = $namespace' \
  | kubectl apply -f -

echo "2) Waiting for cert-manager to issue the kcp client certificate" >&2
kubectl -n "$NAMESPACE" wait --for=condition=Ready \
  "certificates.cert-manager.io/$CERT_NAME" --timeout=120s
# Ready may still reflect the previous certificate immediately after an issuer change.
kubectl -n "$NAMESPACE" wait \
  --for="jsonpath={.metadata.annotations.cert-manager\\.io/issuer-name}=$CLIENT_ISSUER" \
  "secret/$SECRET_NAME" --timeout=120s

client_cert="$(kubectl -n "$NAMESPACE" get secret "$SECRET_NAME" -o jsonpath='{.data.tls\.crt}')"
client_key="$(kubectl -n "$NAMESPACE" get secret "$SECRET_NAME" -o jsonpath='{.data.tls\.key}')"
if [[ -z "$client_cert" || -z "$client_key" ]]; then
  printf 'ERROR: issued Secret is missing the client certificate or key\n' >&2
  exit 1
fi

temp_dir="$(mktemp -d)"
base64 -d <<< "$client_cert" > "$temp_dir/client.crt"
base64 -d <<< "$client_key" > "$temp_dir/client.key"
openssl x509 -in "$temp_dir/client.crt" -noout -checkend 0
subject=$(openssl x509 -in "$temp_dir/client.crt" -noout -subject -nameopt RFC2253)
subject=${subject#subject=}
subject=${subject# }
if [[ ! "$subject" =~ (^|[,+])O=system:masters($|[,+]) ]]; then
  printf 'ERROR: issued certificate lacks system:masters admin privileges: %s\n' "$subject" >&2
  exit 1
fi

if [[ "$PORT_FORWARD" == true ]]; then
  port=8443
  while IFS= read -r service; do
    log="$temp_dir/port-forward-$port.log"
    kubectl -n "$NAMESPACE" port-forward --address 127.0.0.1 "svc/$service" "$port:6443" >"$log" 2>&1 &
    pid=$!
    forward_pids+=("$pid")
    forward_services+=("$service")
    forward_ports+=("$port")
    ready=false
    for ((attempt=0; attempt<100; attempt++)); do
      if ! kill -0 "$pid" 2>/dev/null; then
        printf 'ERROR: port-forward for %s exited\n' "$service" >&2
        cat "$log" >&2
        exit 1
      fi
      if grep -q "Forwarding from 127.0.0.1:$port " "$log"; then
        ready=true
        break
      fi
      sleep 0.1
    done
    if [[ "$ready" != true ]]; then
      printf 'ERROR: port-forward for %s did not become ready within 10 seconds\n' "$service" >&2
      cat "$log" >&2
      exit 1
    fi
    printf 'Forwarding svc/%s at https://127.0.0.1:%s\n' "$service" "$port" >&2
    port=$((port + 1))
  done <<< "$forward_plan"
fi

# Build a fresh file so old CA, SNI, token, or exec credentials cannot survive.
config_file="$(mktemp "${OUT_KUBECONFIG}.XXXXXX")"
echo "3) Assembling kubeconfig (server=$SERVER_URL; insecure-skip-tls-verify=$INSECURE)" >&2
if [[ "$INSECURE" == true ]]; then
  printf 'WARNING: server TLS certificate verification is disabled.\n' >&2
fi
kubectl --kubeconfig="$config_file" config set-cluster kcp \
  --server="$SERVER_URL" --insecure-skip-tls-verify="$INSECURE" >/dev/null
kubectl --kubeconfig="$config_file" config set-credentials "$CONTEXT_NAME" \
  --embed-certs=true \
  --client-certificate="$temp_dir/client.crt" \
  --client-key="$temp_dir/client.key" >/dev/null
kubectl --kubeconfig="$config_file" config set-context "$CONTEXT_NAME" \
  --cluster=kcp --user="$CONTEXT_NAME" >/dev/null
kubectl --kubeconfig="$config_file" config use-context "$CONTEXT_NAME" >/dev/null

echo "4) Verifying admin access through the ingress" >&2
if ! kubectl --kubeconfig="$config_file" --request-timeout=30s \
  auth can-i '*' '*' --all-namespaces; then
  printf 'ERROR: ingress admin access failed; check client CA trust and TLS passthrough. Output was not replaced.\n' >&2
  exit 1
fi
if [[ "$PORT_FORWARD" == true ]]; then
  if ! kubectl --kubeconfig="$config_file" --server="${SERVER_URL%/clusters/root}" --request-timeout=30s \
    get --raw='/clusters/*/apis/core.kcp.io/v1alpha1/logicalclusters' \
    | jq -e '.kind == "LogicalClusterList" and (.items | type == "array")' >/dev/null; then
    printf 'ERROR: direct-shard wildcard access failed. Output was not replaced.\n' >&2
    exit 1
  fi
fi

chmod 600 "$config_file"
mv "$config_file" "$OUT_KUBECONFIG"
config_file=""
echo "Admin kubeconfig written to $OUT_KUBECONFIG" >&2

if [[ "$PORT_FORWARD" == true ]]; then
  shards=$(kubectl --kubeconfig="$OUT_KUBECONFIG" --request-timeout=30s get shards.core.kcp.io -o json)
  echo "Scanner endpoint overrides:" >&2
  for ((index=0; index<${#forward_services[@]}; index++)); do
    service=${forward_services[$index]}
    shard=$(jq -er --arg service "$service" --argjson index "$index" '
      [.items[] | select(
        (if $index == 0 then .metadata.name == "root" else
          (.spec.baseURL | sub("^https://"; "") | split("/")[0] | split(":")[0] | split(".")[0]) == $service
        end)) | .metadata.name]
      | if length == 1 then .[0] else error("cannot uniquely map service to shard") end
    ' <<< "$shards")
    printf '  --shard-server %s=https://127.0.0.1:%s\n' "$shard" "${forward_ports[$index]}" >&2
  done
  echo "Port-forwards running. Press Ctrl+C to stop them." >&2
  while :; do
    for ((index=0; index<${#forward_pids[@]}; index++)); do
      if ! kill -0 "${forward_pids[$index]}" 2>/dev/null; then
        printf 'ERROR: port-forward for %s exited\n' "${forward_services[$index]}" >&2
        cat "$temp_dir/port-forward-${forward_ports[$index]}.log" >&2
        exit 1
      fi
    done
    sleep 1 &
    monitor_pid=$!
    wait "$monitor_pid"
    monitor_pid=""
  done
fi

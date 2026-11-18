#!/usr/bin/env bash

set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
workspace_count="${1:-100}"
if [[ "$#" -gt 1 || ! "$workspace_count" =~ ^[1-9][0-9]*$ ]]; then
  log "ERROR: usage: $0 [positive-workspace-count]"
  exit 2
fi

require_commands kubectl
for ((i = 1; i <= 10#$workspace_count; i++)); do
  kubectl ws create --kubeconfig="${ADMIN_KUBECONFIG}" "team-${i}"
done

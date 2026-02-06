#!/bin/bash
# cleanup-kind-cluster.sh - Cleans up Kind cluster and resources
#
# Note: When running via 'make openshift-test-cleanup', the Makefile adds bin/ to PATH
set -euo pipefail

KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-spark-operator}"
KIND_KUBE_CONFIG="${KIND_KUBE_CONFIG:-$HOME/.kube/config}"

log() { echo "➡️  $1"; }
pass() { echo "✅ $1"; }

# ==========================================
# Step 1: Delete namespace
# ==========================================
log "Deleting namespace..."
kubectl delete namespace spark-operator --ignore-not-found --wait=false 2>/dev/null || true

# ===========================================
# Step 2: Delete Kind cluster
# ===========================================
log "Deleting Kind cluster: $KIND_CLUSTER_NAME"
if command -v kind &>/dev/null; then
    kind delete cluster --name "$KIND_CLUSTER_NAME" --kubeconfig "$KIND_KUBE_CONFIG" 2>/dev/null || true
else
    log "Warning: kind not found in PATH, skipping cluster deletion"
fi

pass "Cleanup complete!"
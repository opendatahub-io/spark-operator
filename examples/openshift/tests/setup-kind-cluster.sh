#!/bin/bash
# setup-kind-cluster.sh - Sets up Kind cluster for OpenShift e2e tests
#
# Usage:
#   ./setup-kind-cluster.sh                    # Basic setup
#   ./setup-kind-cluster.sh --with-docling     # Also load docling-spark image
#   ./setup-kind-cluster.sh --upload-assets    # Also upload test PDFs
#
# Note: When running via 'make openshift-test-setup', the Makefile adds bin/ to PATH

set -euo pipefail # Exit immediately on non-zero status

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" # Script directory
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)" # Repository root

# Configuration (can be overridden by environment)
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-spark-operator}" # Default cluster name
KIND_CONFIG_FILE="${KIND_CONFIG_FILE:-$REPO_ROOT/charts/spark-operator-chart/ci/kind-config.yaml}" # Kind config file
KIND_KUBE_CONFIG="${KIND_KUBE_CONFIG:-$HOME/.kube/config}" # Kind kubeconfig file where connection credentials are stored
K8S_VERSION="${K8S_VERSION:-v1.32.0}" # Kubernetes version
SPARK_NAMESPACE="${SPARK_NAMESPACE:-spark-operator}" # Namespace for Spark Operator and apps
DOCLING_IMAGE="${DOCLING_IMAGE:-quay.io/rishasin/docling-spark:latest}" # Docling Spark image

# Verify kind is available
if ! command -v kind &>/dev/null; then
    echo "❌ Error: 'kind' not found in PATH."
    echo "   Run 'make kind' first, or use 'make openshift-test-setup' which sets up PATH automatically."
    exit 1
fi

# Parse arguments
WITH_DOCLING=false
UPLOAD_ASSETS=false
for arg in "$@"; do # Loop through all arguments
    case "$arg" in # Check if the argument is a valid option
        --with-docling) WITH_DOCLING=true ;; # If present, sets a flag to pull and load a large (9.5GB) Docker image.
        --upload-assets) UPLOAD_ASSETS=true ;; # If present, sets a flag to upload test PDFs to the Kind cluster.
    esac
done

log() { echo "➡️  $1"; } # Log a message to the console.
pass() { echo "✅ $1"; } # Log a success message to the console.
fail() { echo "❌ $1"; exit 1; } # Exit the script with a failure status.

# =================================================
# Step 1: Create Kind cluster (if not exists)
# =================================================
log "Checking for existing Kind cluster..."
if kind get clusters 2>/dev/null | grep -q "^${KIND_CLUSTER_NAME}$"; then
    log "Kind cluster '${KIND_CLUSTER_NAME}' already exists"
else
    log "Creating Kind cluster '${KIND_CLUSTER_NAME}'..."
    kind create cluster \
        --name "$KIND_CLUSTER_NAME" \
        --config "$KIND_CONFIG_FILE" \
        --image "kindest/node:${K8S_VERSION}" \
        --kubeconfig "$KIND_KUBE_CONFIG" \
        --wait=1m
    pass "Kind cluster created"
fi

# Verify kubectl works
kubectl cluster-info || fail "Cannot connect to cluster"

# =================================================
# Step 2: Create namespace
# =================================================
log "Creating namespace: $SPARK_NAMESPACE"
kubectl create namespace "$SPARK_NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
pass "Namespace created"

# =================================================
# Step 3: Create PVCs (transform for Kind - remove storageClassName)
# =================================================
log "Creating PVCs (transformed for Kind's default StorageClass)..."
sed '/storageClassName:/d' "$REPO_ROOT/examples/openshift/k8s/docling-input-pvc.yaml" | kubectl apply -f -
sed '/storageClassName:/d' "$REPO_ROOT/examples/openshift/k8s/docling-output-pvc.yaml" | kubectl apply -f -
pass "PVCs created"

# =================================================
# Step 4: Apply RBAC
# =================================================
log "Applying RBAC..."
kubectl apply -f "$REPO_ROOT/examples/openshift/k8s/base/rbac.yaml"
pass "RBAC applied"

# =================================================
# Step 5 (Optional): Load docling-spark image
# =================================================
if [ "$WITH_DOCLING" = true ]; then
    log "Pulling docling-spark image (~9.5GB, this may take a while)..."
    docker pull "$DOCLING_IMAGE"
    
    log "Loading image into Kind cluster..."
    kind load docker-image "$DOCLING_IMAGE" --name "$KIND_CLUSTER_NAME"
    pass "Docling image loaded"
fi

# =================================================
# Step 6 (Optional): Upload test assets
# =================================================
if [ "$UPLOAD_ASSETS" = true ]; then
    log "Uploading test PDFs..."
    cd "$REPO_ROOT/examples/openshift"
    ./k8s/deploy.sh upload ./tests/assets/
    pass "Test assets uploaded"
fi

# =================================================
# Summary
# =================================================
echo ""
echo "============================================"
pass "KIND CLUSTER SETUP COMPLETE!"
echo "============================================"
echo ""
echo "Cluster: $KIND_CLUSTER_NAME"
echo "Namespace: $SPARK_NAMESPACE"
echo "Kubeconfig: $KIND_KUBE_CONFIG"
echo ""
echo "Next steps:"
echo "  1. Install operator:  make operator-install"
echo "  2. Run Spark Pi:      make test-spark-pi"
echo "  3. Run Docling:       make test-docling-spark"
echo "  4. Run all tests:     make test-all"
echo ""
echo "  Or run Go tests:      make openshift-e2e-test"
echo ""
echo "  Cleanup when done:    make kind-cleanup"
echo ""
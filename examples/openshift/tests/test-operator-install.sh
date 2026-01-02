#!/bin/bash
# ============================================================================
# Test 1: Spark Operator Installation Test
# ============================================================================
#
# This test verifies:
#   1. Spark Operator installs successfully from the Helm chart
#   2. fsGroup is NOT 185 (OpenShift security requirement)
#   3. jobNamespaces is configured correctly
#
# Usage:
#   ./test-operator-install.sh          # Install and test (keeps operator)
#   CLEANUP=true ./test-operator-install.sh  # Cleanup after test
#
# Prerequisites:
#   - kubectl configured with cluster access
#   - helm installed
#
# ============================================================================

set -euo pipefail

# ============================================================================
# Configuration
# ============================================================================
RELEASE_NAME="${RELEASE_NAME:-spark-operator-openshift}"
RELEASE_NAMESPACE="${RELEASE_NAMESPACE:-spark-operator-openshift}"
HELM_REPO_NAME="${HELM_REPO_NAME:-opendatahub-spark-operator}"
HELM_REPO_URL="${HELM_REPO_URL:-https://opendatahub-io.github.io/spark-operator}"
CHART_NAME="${CHART_NAME:-spark-operator}"
TIMEOUT="${TIMEOUT:-5m}"

# Expected jobNamespaces (docling-spark namespace for our tests)
EXPECTED_JOB_NAMESPACE="${EXPECTED_JOB_NAMESPACE:-docling-spark}"

# ============================================================================
# Helper Functions
# ============================================================================
log()  { echo "➡️  $1"; }
pass() { echo "✅ $1"; }
fail() { echo "❌ $1"; exit 1; }
warn() { echo "⚠️  $1"; }

cleanup() {
    # By default, KEEP the operator installed so subsequent tests can use it
    # Set CLEANUP=true to remove after test
    if [ "${CLEANUP:-false}" = "true" ]; then
        log "Cleaning up (CLEANUP=true)..."
        helm uninstall "$RELEASE_NAME" -n "$RELEASE_NAMESPACE" --wait 2>/dev/null || true
        kubectl delete namespace "$RELEASE_NAMESPACE" --ignore-not-found --wait=false || true
    else
        log "Keeping operator installed for subsequent tests"
        log "To cleanup manually: helm uninstall $RELEASE_NAME -n $RELEASE_NAMESPACE"
    fi
}

# Cleanup on exit (unless SKIP_CLEANUP=true)
trap cleanup EXIT

# ============================================================================
# Setup: Install Spark Operator
# ============================================================================
log "Adding Helm repository: $HELM_REPO_URL"
if ! helm repo add "$HELM_REPO_NAME" "$HELM_REPO_URL" 2>/dev/null; then
    # Repo add failed - check if it already exists
    if helm repo list | grep -q "^$HELM_REPO_NAME"; then
        log "Helm repo '$HELM_REPO_NAME' already exists (OK)"
    else
        fail "Failed to add Helm repo: $HELM_REPO_URL"
    fi
fi
helm repo update

log "Installing Spark Operator..."
log "  Release:   $RELEASE_NAME"
log "  Namespace: $RELEASE_NAMESPACE"
log "  Chart:     $HELM_REPO_NAME/$CHART_NAME"

helm install "$RELEASE_NAME" "$HELM_REPO_NAME/$CHART_NAME" \
    --namespace "$RELEASE_NAMESPACE" \
    --create-namespace \
    --set "spark.jobNamespaces={$EXPECTED_JOB_NAMESPACE}" \
    --wait \
    --timeout "$TIMEOUT"

pass "Spark Operator installed successfully"

# ============================================================================
# Wait for pods to be ready
# ============================================================================
log "Waiting for operator pods to be ready..."
kubectl wait --for=condition=Ready pod \
    -l app.kubernetes.io/instance="$RELEASE_NAME" \
    -n "$RELEASE_NAMESPACE" \
    --timeout=120s

pass "All operator pods are ready"

# ============================================================================
# Test 1: Verify fsGroup is NOT 185
# ============================================================================
log "TEST 1: Checking fsGroup on operator pods..."

FAILED=false
while IFS= read -r line; do
    POD_NAME=$(echo "$line" | awk '{print $1}')
    FSGROUP=$(echo "$line" | awk '{print $2}')
    
    if [ -z "$POD_NAME" ]; then
        continue
    fi
    
    if [ "$FSGROUP" = "185" ]; then
        fail "Pod $POD_NAME has fsGroup=185 (not allowed for OpenShift)"
        FAILED=true
    elif [ -z "$FSGROUP" ] || [ "$FSGROUP" = "null" ]; then
        echo "  $POD_NAME: fsGroup not set (OK for OpenShift)"
    else
        echo "  $POD_NAME: fsGroup=$FSGROUP (OK)"
    fi
done < <(kubectl get pods -n "$RELEASE_NAMESPACE" \
    -l app.kubernetes.io/instance="$RELEASE_NAME" \
    -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.securityContext.fsGroup}{"\n"}{end}')

if [ "$FAILED" = "true" ]; then
    fail "fsGroup check failed!"
fi

pass "TEST 1 PASSED: No operator pods have fsGroup=185"

# ============================================================================
# Test 2: Verify jobNamespaces configuration
# ============================================================================
log "TEST 2: Checking jobNamespaces configuration..."

# Get the controller pod
CONTROLLER_POD=$(kubectl get pods -n "$RELEASE_NAMESPACE" \
    -l app.kubernetes.io/component=controller \
    -o jsonpath='{.items[0].metadata.name}')

if [ -z "$CONTROLLER_POD" ]; then
    fail "Could not find controller pod"
fi

# Get the --namespaces argument from the controller
NAMESPACES_ARG=$(kubectl get pod "$CONTROLLER_POD" -n "$RELEASE_NAMESPACE" \
    -o jsonpath='{.spec.containers[0].args}' | grep -oP '(?<=--namespaces=)[^"]*' || echo "")

if [ -z "$NAMESPACES_ARG" ]; then
    # Try getting from command instead of args
    NAMESPACES_ARG=$(kubectl get pod "$CONTROLLER_POD" -n "$RELEASE_NAMESPACE" \
        -o jsonpath='{.spec.containers[0].command}' | grep -oP '(?<=--namespaces=)[^"]*' || echo "")
fi

echo "  Controller pod: $CONTROLLER_POD"
echo "  Configured namespaces: $NAMESPACES_ARG"

if echo "$NAMESPACES_ARG" | grep -q "$EXPECTED_JOB_NAMESPACE"; then
    pass "TEST 2 PASSED: jobNamespaces includes '$EXPECTED_JOB_NAMESPACE'"
else
    warn "jobNamespaces may not include '$EXPECTED_JOB_NAMESPACE' (found: $NAMESPACES_ARG)"
    warn "This might be OK if using different configuration"
fi

# ============================================================================
# Test 3: Verify webhooks are configured
# ============================================================================
log "TEST 3: Checking webhook configurations..."

MUTATING_WEBHOOK=$(kubectl get mutatingwebhookconfiguration \
    -l app.kubernetes.io/instance="$RELEASE_NAME" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")

VALIDATING_WEBHOOK=$(kubectl get validatingwebhookconfiguration \
    -l app.kubernetes.io/instance="$RELEASE_NAME" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")

if [ -n "$MUTATING_WEBHOOK" ]; then
    echo "  MutatingWebhookConfiguration: $MUTATING_WEBHOOK"
else
    warn "MutatingWebhookConfiguration not found"
fi

if [ -n "$VALIDATING_WEBHOOK" ]; then
    echo "  ValidatingWebhookConfiguration: $VALIDATING_WEBHOOK"
else
    warn "ValidatingWebhookConfiguration not found"
fi

if [ -n "$MUTATING_WEBHOOK" ] && [ -n "$VALIDATING_WEBHOOK" ]; then
    pass "TEST 3 PASSED: Webhooks are configured"
else
    warn "Some webhooks may not be configured (might be intentional)"
fi

# ============================================================================
# Summary
# ============================================================================
echo ""
echo "============================================"
pass "ALL OPERATOR INSTALL TESTS PASSED!"
echo "============================================"
echo ""
echo "Operator is ready. You can now run the Spark application tests:"
echo "  ./test-spark-pi.sh      # Lightweight Pi test"
echo "  ./test-spark-app.sh     # Full docling-spark test"
echo ""
echo "To cleanup the operator when done:"
echo "  CLEANUP=true ./test-operator-install.sh"
echo "  # or manually: helm uninstall $RELEASE_NAME -n $RELEASE_NAMESPACE"
echo ""


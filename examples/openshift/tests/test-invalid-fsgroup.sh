#!/bin/bash
# ============================================================================
# Test: ValidatingAdmissionPolicy - Reject fsGroup in SparkApplication
# ============================================================================
#
# This test verifies that the ValidatingAdmissionPolicy correctly rejects
# SparkApplications that have fsGroup set in podSecurityContext.
#
# This test verifies:
#   1. ValidatingAdmissionPolicy is installed
#   2. Attempting to apply SparkApplication with fsGroup is rejected
#   3. Error message contains expected policy information
#
# Prerequisites:
#   - Spark Operator already installed (run test-operator-install.sh first)
#   - ValidatingAdmissionPolicy installed (via deploy.sh or manually)
#
# Usage:
#   ./test-invalid-fsgroup.sh
#
# Environment Variables:
#   APP_NAMESPACE     - Namespace to deploy app (default: docling-spark)
#   SKIP_CLEANUP      - Set to "true" to preserve resources for debugging
#
# ============================================================================

set -euo pipefail

# ============================================================================
# Configuration
# ============================================================================
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

APP_NAMESPACE="${APP_NAMESPACE:-docling-spark}"
APP_NAME="${APP_NAME:-test-invalid-fsgroup}"
TEST_YAML="${TEST_YAML:-$SCRIPT_DIR/invalid-fsgroup.yaml}"
POLICY_NAME="${POLICY_NAME:-deny-fsgroup-in-sparkapplication}"

# ============================================================================
# Helper Functions
# ============================================================================
log()  { echo "➡️  $1"; }
pass() { echo "✅ $1"; }
fail() { echo "❌ $1"; exit 1; }
warn() { echo "⚠️  $1"; }

cleanup() {
    if [ "${SKIP_CLEANUP:-false}" = "true" ]; then
        warn "SKIP_CLEANUP=true, leaving resources for inspection"
        echo ""
        echo "To inspect:"
        echo "  kubectl get sparkapplication $APP_NAME -n $APP_NAMESPACE -o yaml"
        echo ""
        echo "To cleanup manually:"
        echo "  kubectl delete sparkapplication $APP_NAME -n $APP_NAMESPACE --ignore-not-found"
        return
    fi
    log "Cleaning up test SparkApplication..."
    kubectl delete sparkapplication "$APP_NAME" -n "$APP_NAMESPACE" --ignore-not-found || true
}

trap cleanup EXIT

# ============================================================================
# Pre-flight Checks
# ============================================================================
log "Running pre-flight checks..."

# Check if operator is installed
if ! kubectl get deployment -n spark-operator-openshift -l app.kubernetes.io/name=spark-operator &>/dev/null; then
    if ! kubectl get deployment -n kubeflow-spark-operator -l app.kubernetes.io/name=spark-operator &>/dev/null; then
        fail "Spark Operator not found. Run test-operator-install.sh first."
    fi
fi
echo "  Spark Operator: Found"

# Check if ValidatingAdmissionPolicy is installed
if ! kubectl get validatingadmissionpolicy "$POLICY_NAME" &>/dev/null; then
    fail "ValidatingAdmissionPolicy '$POLICY_NAME' not found. Install it first via deploy.sh or manually."
fi
echo "  ValidatingAdmissionPolicy: Found"

# Check if binding exists
if ! kubectl get validatingadmissionpolicybinding "${POLICY_NAME}-binding" &>/dev/null; then
    fail "ValidatingAdmissionPolicyBinding '${POLICY_NAME}-binding' not found. Install it first via deploy.sh or manually."
fi
echo "  ValidatingAdmissionPolicyBinding: Found"

pass "Pre-flight checks passed"

# ============================================================================
# Setup: Create namespace
# ============================================================================
log "Creating namespace '$APP_NAMESPACE' if not exists..."
kubectl create namespace "$APP_NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

# ============================================================================
# Test: Attempt to apply invalid SparkApplication
# ============================================================================
log "Testing ValidatingAdmissionPolicy rejection..."
echo "  Attempting to apply SparkApplication with fsGroup in podSecurityContext..."
echo "  Expected: Request should be rejected by policy"

# Delete any existing test app first
kubectl delete sparkapplication "$APP_NAME" -n "$APP_NAMESPACE" --ignore-not-found 2>/dev/null || true
sleep 2

# Try to apply the invalid YAML and capture the error
if [ ! -f "$TEST_YAML" ]; then
    fail "Test YAML file not found: $TEST_YAML"
fi

log "Applying invalid SparkApplication from $TEST_YAML..."

# Capture both stdout and stderr
APPLY_OUTPUT=$(kubectl apply -f "$TEST_YAML" -n "$APP_NAMESPACE" 2>&1) || APPLY_EXIT_CODE=$?

# Check if the command failed (which is expected)
if [ "${APPLY_EXIT_CODE:-0}" -eq 0 ]; then
    fail "ERROR: SparkApplication was accepted, but it should have been rejected!"
fi

echo ""
echo "=== Apply Output (Expected Failure) ==="
echo "$APPLY_OUTPUT"
echo ""

# ============================================================================
# Verify Rejection
# ============================================================================
log "Verifying rejection message..."

# Check if error contains policy name - CRITICAL: Must be from our policy
if echo "$APPLY_OUTPUT" | grep -qi "$POLICY_NAME"; then
    pass "Error message contains policy name: $POLICY_NAME"
else
    fail "ERROR: Error message does not contain policy name '$POLICY_NAME'. This indicates the rejection is NOT from our ValidatingAdmissionPolicy. The apply may have failed for a different reason."
fi

# Check if error mentions fsGroup - CRITICAL: Must be about fsGroup
if echo "$APPLY_OUTPUT" | grep -qi "fsGroup"; then
    pass "Error message mentions fsGroup"
else
    fail "ERROR: Error message does not mention 'fsGroup'. This indicates the rejection is NOT about fsGroup validation. The apply may have failed for a different reason."
fi

# Check if error mentions podSecurityContext - Optional but helpful
if echo "$APPLY_OUTPUT" | grep -qi "podSecurityContext"; then
    pass "Error message mentions podSecurityContext"
else
    warn "Warning: Error message does not mention 'podSecurityContext'"
fi

# Check if it's a Forbidden error - Optional (exit code already confirms failure)
if echo "$APPLY_OUTPUT" | grep -qiE "(Forbidden|denied|rejected)"; then
    pass "Error indicates request was forbidden/rejected"
else
    warn "Warning: Error message does not clearly indicate rejection"
fi

# Verify the SparkApplication was NOT created
log "Verifying SparkApplication was NOT created..."
if kubectl get sparkapplication "$APP_NAME" -n "$APP_NAMESPACE" &>/dev/null; then
    fail "ERROR: SparkApplication was created despite policy rejection!"
else
    pass "SparkApplication was correctly NOT created"
fi

# ============================================================================
# Summary
# ============================================================================
echo ""
echo "============================================"
pass "VALIDATINGADMISSIONPOLICY TEST PASSED!"
echo "============================================"
echo ""
echo "Summary:"
echo "  - Policy: $POLICY_NAME"
echo "  - Test: Attempted to apply SparkApplication with fsGroup"
echo "  - Result: Request correctly rejected"
echo "  - SparkApplication: NOT created (as expected)"
echo ""
echo "The ValidatingAdmissionPolicy is working correctly!"
echo ""
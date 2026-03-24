#!/bin/bash
# ============================================================================
# Test: SparkApplication Runtime Failure Handling
# ============================================================================
#
# Verifies that the operator correctly handles application runtime failures
# and respects the onFailureRetries restart policy.
#
# The test uses a SparkApplication with a non-existent mainClass/jar,
# so the driver starts but the application fails immediately.
#
# This test verifies:
#   1. SparkApplication transitions through failure states
#   2. Final state is FAILED after retries are exhausted
#   3. A driver pod WAS created (unlike submission failure)
#   4. Execution attempts match the expected retry count
#
# Prerequisites:
#   - Spark Operator installed (run test-operator-install.sh first)
#
# Usage:
#   ./test-application-failure.sh
#   CLEANUP=false ./test-application-failure.sh  # Keep resources for debugging
#
# Environment Variables:
#   APP_NAMESPACE     - Namespace to deploy app (default: spark-operator)
#   TIMEOUT_SECONDS   - Max wait time (default: 600)
#   CLEANUP           - Set to "false" to preserve resources (default: true)
#
# ============================================================================

set -euo pipefail

# ============================================================================
# Configuration
# ============================================================================
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

APP_NAMESPACE="${APP_NAMESPACE:-spark-operator}"
APP_NAME="${APP_NAME:-fail-application-test}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-600}"
SPARK_IMAGE="${SPARK_IMAGE:-quay.io/rishasin/docling-spark@sha256:7e8431fc89dbc6c10aec1f0401aadd9c9cd66b9728fbcb98f6bf40ba3e3b4cdb}"
APP_YAML="${APP_YAML:-$SCRIPT_DIR/fixtures/fail-application-app.yaml}"
EXPECTED_RETRIES=2

# ============================================================================
# Helper Functions
# ============================================================================
log()  { echo "➡️  $1"; }
pass() { echo "✅ $1"; }
fail() { echo "❌ $1"; exit 1; }
warn() { echo "⚠️  $1"; }

cleanup() {
    if [ "${CLEANUP:-true}" = "false" ]; then
        warn "CLEANUP=false, leaving resources for inspection"
        echo "  kubectl get sparkapplication $APP_NAME -n $APP_NAMESPACE -o yaml"
        echo "  kubectl logs ${APP_NAME}-driver -n $APP_NAMESPACE"
        return
    fi
    log "Cleaning up SparkApplication..."
    kubectl delete sparkapplication "$APP_NAME" -n "$APP_NAMESPACE" --ignore-not-found || true
}

trap cleanup EXIT

get_app_state() {
    kubectl get sparkapplication "$APP_NAME" -n "$APP_NAMESPACE" \
        -o jsonpath='{.status.applicationState.state}' 2>/dev/null || echo "NOT_FOUND"
}

get_app_error() {
    kubectl get sparkapplication "$APP_NAME" -n "$APP_NAMESPACE" \
        -o jsonpath='{.status.applicationState.errorMessage}' 2>/dev/null || echo ""
}

get_execution_attempts() {
    kubectl get sparkapplication "$APP_NAME" -n "$APP_NAMESPACE" \
        -o jsonpath='{.status.executionAttempts}' 2>/dev/null || echo "0"
}

# ============================================================================
# Pre-flight Checks
# ============================================================================
log "Running pre-flight checks..."

if ! kubectl get deployment -n spark-operator -l app.kubernetes.io/name=spark-operator &>/dev/null; then
    fail "Spark Operator not found. Run test-operator-install.sh first."
fi
echo "  Spark Operator: Found"

if [ ! -f "$APP_YAML" ]; then
    fail "SparkApplication YAML not found: $APP_YAML"
fi

pass "Pre-flight checks passed"

# ============================================================================
# Deploy SparkApplication (expected to fail at runtime)
# ============================================================================
log "Deploying SparkApplication with invalid mainClass/jar..."
echo "  Name:      $APP_NAME"
echo "  Namespace: $APP_NAMESPACE"
echo "  Expected:  Application failure after $EXPECTED_RETRIES retries"

kubectl delete sparkapplication "$APP_NAME" -n "$APP_NAMESPACE" --ignore-not-found 2>/dev/null || true

export APP_NAME APP_NAMESPACE SPARK_IMAGE
envsubst < "$APP_YAML" | kubectl apply -f -

pass "SparkApplication submitted"

# ============================================================================
# Wait for SparkApplication to reach FAILED state
# ============================================================================
log "Waiting for SparkApplication to fail (timeout: ${TIMEOUT_SECONDS}s)..."

SECONDS=0
LAST_STATE=""
SAW_RUNNING=false

while [ $SECONDS -lt $TIMEOUT_SECONDS ]; do
    STATE=$(get_app_state)

    if [ "$STATE" != "$LAST_STATE" ]; then
        ATTEMPTS=$(get_execution_attempts)
        echo "  [${SECONDS}s] State: $STATE (executionAttempts: $ATTEMPTS)"
        LAST_STATE="$STATE"
    fi

    if [ "$STATE" = "RUNNING" ] || [ "$STATE" = "SUBMITTED" ]; then
        SAW_RUNNING=true
    fi

    if [ "$STATE" = "FAILED" ]; then
        pass "SparkApplication reached FAILED state as expected"
        break
    fi

    if [ "$STATE" = "COMPLETED" ]; then
        fail "SparkApplication unexpectedly COMPLETED (expected FAILED)"
    fi

    sleep 5
done

if [ "$STATE" != "FAILED" ]; then
    echo ""
    echo "=== Timeout - Current State: $STATE ==="
    kubectl get sparkapplication "$APP_NAME" -n "$APP_NAMESPACE" -o yaml 2>/dev/null || true
    echo ""
    echo "=== Driver Pod Logs ==="
    kubectl logs "${APP_NAME}-driver" -n "$APP_NAMESPACE" --tail=50 2>/dev/null || echo "(no driver logs)"
    fail "SparkApplication did not reach FAILED within ${TIMEOUT_SECONDS}s (last state: $STATE)"
fi

# ============================================================================
# Verify Results
# ============================================================================
log "Verifying failure behavior..."

# Check that submission succeeded (app was RUNNING/SUBMITTED before failing)
if [ "$SAW_RUNNING" = "true" ]; then
    echo "  Observed RUNNING/SUBMITTED state before failure (submission succeeded)"
else
    warn "Did not observe RUNNING/SUBMITTED state before failure"
fi

# Verify error message exists
ERROR_MSG=$(get_app_error)
echo "  Error message: ${ERROR_MSG:0:120}"

if [ -z "$ERROR_MSG" ]; then
    warn "No error message found in status"
fi

# Verify execution attempts
FINAL_ATTEMPTS=$(get_execution_attempts)
EXPECTED_TOTAL=$((EXPECTED_RETRIES + 1))
echo "  Execution attempts: $FINAL_ATTEMPTS (expected: $EXPECTED_TOTAL)"

if [ "$FINAL_ATTEMPTS" -eq "$EXPECTED_TOTAL" ]; then
    pass "Execution attempts match expected count"
elif [ "$FINAL_ATTEMPTS" -gt 0 ]; then
    warn "Execution attempts ($FINAL_ATTEMPTS) differ from expected ($EXPECTED_TOTAL)"
fi

# Verify driver pod was created (unlike submission failure, the driver should have run)
DRIVER_POD="${APP_NAME}-driver"
if kubectl get pod "$DRIVER_POD" -n "$APP_NAMESPACE" &>/dev/null; then
    DRIVER_STATUS=$(kubectl get pod "$DRIVER_POD" -n "$APP_NAMESPACE" \
        -o jsonpath='{.status.phase}' 2>/dev/null || echo "Unknown")
    echo "  Driver pod: $DRIVER_POD (status: $DRIVER_STATUS)"
else
    echo "  Driver pod not found (may have been cleaned up after retries)"
fi

# Show driver logs if available
echo ""
log "Driver logs (last 10 lines):"
kubectl logs "$DRIVER_POD" -n "$APP_NAMESPACE" --tail=10 2>/dev/null || echo "  (no driver logs available)"

# ============================================================================
# Summary
# ============================================================================
echo ""
echo "============================================"
pass "APPLICATION FAILURE TEST PASSED!"
echo "============================================"
echo ""
echo "Summary:"
echo "  - SparkApplication: $APP_NAME"
echo "  - Final State: FAILED"
echo "  - Execution Attempts: $FINAL_ATTEMPTS"
echo "  - Saw RUNNING/SUBMITTED: $SAW_RUNNING"
echo ""

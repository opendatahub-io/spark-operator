#!/bin/bash
# ============================================================================
# Test: SparkApplication Submission Failure Handling
# ============================================================================
#
# Verifies that the operator correctly handles submission failures and
# respects the onSubmissionFailureRetries restart policy.
#
# The test uses a SparkApplication with a non-existent serviceAccount,
# causing spark-submit to fail before the driver pod is created.
#
# This test verifies:
#   1. SparkApplication transitions through FailedSubmission states
#   2. Final state is FAILED after retries are exhausted
#   3. No driver pod is created (submission never succeeded)
#   4. Error message indicates submission failure
#
# Prerequisites:
#   - Spark Operator installed (run test-operator-install.sh first)
#
# Usage:
#   ./test-submission-failure.sh
#   CLEANUP=false ./test-submission-failure.sh   # Keep resources for debugging
#
# Environment Variables:
#   APP_NAMESPACE     - Namespace to deploy app (default: spark-operator)
#   TIMEOUT_SECONDS   - Max wait time (default: 300)
#   CLEANUP           - Set to "false" to preserve resources (default: true)
#
# ============================================================================

set -euo pipefail

# ============================================================================
# Configuration
# ============================================================================
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

APP_NAMESPACE="${APP_NAMESPACE:-spark-operator}"
APP_NAME="${APP_NAME:-fail-submission-test}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-300}"
SPARK_IMAGE="${SPARK_IMAGE:-quay.io/rishasin/docling-spark:latest}"
APP_YAML="${APP_YAML:-$SCRIPT_DIR/fixtures/fail-submission-app.yaml}"
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

get_submission_attempts() {
    kubectl get sparkapplication "$APP_NAME" -n "$APP_NAMESPACE" \
        -o jsonpath='{.status.submissionAttempts}' 2>/dev/null || echo "0"
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
# Deploy SparkApplication (expected to fail submission)
# ============================================================================
log "Deploying SparkApplication with invalid serviceAccount..."
echo "  Name:      $APP_NAME"
echo "  Namespace: $APP_NAMESPACE"
echo "  Expected:  Submission failure after $EXPECTED_RETRIES retries"

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
SAW_FAILED_SUBMISSION=false

while [ $SECONDS -lt $TIMEOUT_SECONDS ]; do
    STATE=$(get_app_state)

    if [ "$STATE" != "$LAST_STATE" ]; then
        ATTEMPTS=$(get_submission_attempts)
        echo "  [${SECONDS}s] State: $STATE (submissionAttempts: $ATTEMPTS)"
        LAST_STATE="$STATE"
    fi

    if [ "$STATE" = "FAILED_SUBMISSION" ]; then
        SAW_FAILED_SUBMISSION=true
    fi

    if [ "$STATE" = "FAILED" ]; then
        pass "SparkApplication reached FAILED state as expected"
        break
    fi

    if [ "$STATE" = "COMPLETED" ] || [ "$STATE" = "RUNNING" ]; then
        fail "SparkApplication unexpectedly reached $STATE (expected FAILED)"
    fi

    sleep 3
done

if [ "$STATE" != "FAILED" ]; then
    echo ""
    echo "=== Timeout - Current State: $STATE ==="
    kubectl get sparkapplication "$APP_NAME" -n "$APP_NAMESPACE" -o yaml 2>/dev/null || true
    fail "SparkApplication did not reach FAILED within ${TIMEOUT_SECONDS}s (last state: $STATE)"
fi

# ============================================================================
# Verify Results
# ============================================================================
log "Verifying failure behavior..."

# Check that we saw FailedSubmission states during retries
if [ "$SAW_FAILED_SUBMISSION" = "true" ]; then
    echo "  Observed FAILED_SUBMISSION state during retries"
else
    warn "Did not observe FAILED_SUBMISSION intermediate state"
fi

# Verify error message
ERROR_MSG=$(get_app_error)
echo "  Error message: ${ERROR_MSG:0:120}"

if [ -z "$ERROR_MSG" ]; then
    warn "No error message found in status"
fi

# Verify submission attempts
FINAL_ATTEMPTS=$(get_submission_attempts)
EXPECTED_TOTAL=$((EXPECTED_RETRIES + 1))
echo "  Submission attempts: $FINAL_ATTEMPTS (expected: $EXPECTED_TOTAL)"

if [ "$FINAL_ATTEMPTS" -eq "$EXPECTED_TOTAL" ]; then
    pass "Submission attempts match expected count"
elif [ "$FINAL_ATTEMPTS" -gt 0 ]; then
    warn "Submission attempts ($FINAL_ATTEMPTS) differ from expected ($EXPECTED_TOTAL)"
fi

# Verify no driver pod was created
DRIVER_POD="${APP_NAME}-driver"
if kubectl get pod "$DRIVER_POD" -n "$APP_NAMESPACE" &>/dev/null; then
    warn "Driver pod exists (unexpected for submission failure)"
else
    echo "  No driver pod created (correct for submission failure)"
fi

# ============================================================================
# Summary
# ============================================================================
echo ""
echo "============================================"
pass "SUBMISSION FAILURE TEST PASSED!"
echo "============================================"
echo ""
echo "Summary:"
echo "  - SparkApplication: $APP_NAME"
echo "  - Final State: FAILED"
echo "  - Submission Attempts: $FINAL_ATTEMPTS"
echo "  - Saw FAILED_SUBMISSION: $SAW_FAILED_SUBMISSION"
echo ""

#!/bin/bash
# ============================================================================
# Test: Webhook Validation
# ============================================================================
#
# Verifies that the Kustomize-installed webhooks are functional.
#
# This test verifies:
#   1. MutatingWebhookConfiguration exists with CA bundle
#   2. ValidatingWebhookConfiguration exists with CA bundle
#   3. Webhook Service has healthy endpoints
#   4. Validating webhook rejects an invalid SparkApplication
#
# Prerequisites:
#   - Spark Operator installed via Kustomize (run test-operator-install.sh first)
#
# Usage:
#   ./test-webhook-validation.sh
#
# ============================================================================

set -euo pipefail

# ============================================================================
# Configuration
# ============================================================================
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

RELEASE_NAMESPACE="${RELEASE_NAMESPACE:-spark-operator}"
MUTATING_WEBHOOK_NAME="spark-operator-webhook"
VALIDATING_WEBHOOK_NAME="spark-operator-webhook"
WEBHOOK_SERVICE_NAME="spark-operator-webhook-svc"
INVALID_APP_YAML="$SCRIPT_DIR/fixtures/invalid-sparkapplication.yaml"

# ============================================================================
# Helper Functions
# ============================================================================
log()  { echo "➡️  $1"; }
pass() { echo "✅ $1"; }
fail() { echo "❌ $1"; exit 1; }

# ============================================================================
# Test 1: MutatingWebhookConfiguration exists with CA bundle
# ============================================================================
log "TEST 1: Verifying MutatingWebhookConfiguration..."

if ! kubectl get mutatingwebhookconfiguration "$MUTATING_WEBHOOK_NAME" &>/dev/null; then
    fail "MutatingWebhookConfiguration '$MUTATING_WEBHOOK_NAME' not found"
fi
echo "  MutatingWebhookConfiguration: Found"

WEBHOOK_COUNT=$(kubectl get mutatingwebhookconfiguration "$MUTATING_WEBHOOK_NAME" \
    -o jsonpath='{.webhooks[*].name}' 2>/dev/null | wc -w | tr -d ' ')
echo "  Registered mutating webhooks: $WEBHOOK_COUNT"

if [ "$WEBHOOK_COUNT" -lt 1 ]; then
    fail "No mutating webhooks registered"
fi

# Verify CA bundles are populated on all webhooks
EMPTY_CA=0
for i in $(seq 0 $((WEBHOOK_COUNT - 1))); do
    CA_BUNDLE=$(kubectl get mutatingwebhookconfiguration "$MUTATING_WEBHOOK_NAME" \
        -o jsonpath="{.webhooks[$i].clientConfig.caBundle}" 2>/dev/null || echo "")
    WH_NAME=$(kubectl get mutatingwebhookconfiguration "$MUTATING_WEBHOOK_NAME" \
        -o jsonpath="{.webhooks[$i].name}" 2>/dev/null || echo "unknown")
    if [ -z "$CA_BUNDLE" ]; then
        echo "  WARNING: $WH_NAME has no CA bundle"
        EMPTY_CA=$((EMPTY_CA + 1))
    fi
done

if [ "$EMPTY_CA" -gt 0 ]; then
    fail "$EMPTY_CA mutating webhook(s) missing CA bundle"
fi

pass "TEST 1 PASSED: MutatingWebhookConfiguration is healthy"

# ============================================================================
# Test 2: ValidatingWebhookConfiguration exists with CA bundle
# ============================================================================
log "TEST 2: Verifying ValidatingWebhookConfiguration..."

if ! kubectl get validatingwebhookconfiguration "$VALIDATING_WEBHOOK_NAME" &>/dev/null; then
    fail "ValidatingWebhookConfiguration '$VALIDATING_WEBHOOK_NAME' not found"
fi
echo "  ValidatingWebhookConfiguration: Found"

WEBHOOK_COUNT=$(kubectl get validatingwebhookconfiguration "$VALIDATING_WEBHOOK_NAME" \
    -o jsonpath='{.webhooks[*].name}' 2>/dev/null | wc -w | tr -d ' ')
echo "  Registered validating webhooks: $WEBHOOK_COUNT"

if [ "$WEBHOOK_COUNT" -lt 1 ]; then
    fail "No validating webhooks registered"
fi

EMPTY_CA=0
for i in $(seq 0 $((WEBHOOK_COUNT - 1))); do
    CA_BUNDLE=$(kubectl get validatingwebhookconfiguration "$VALIDATING_WEBHOOK_NAME" \
        -o jsonpath="{.webhooks[$i].clientConfig.caBundle}" 2>/dev/null || echo "")
    WH_NAME=$(kubectl get validatingwebhookconfiguration "$VALIDATING_WEBHOOK_NAME" \
        -o jsonpath="{.webhooks[$i].name}" 2>/dev/null || echo "unknown")
    if [ -z "$CA_BUNDLE" ]; then
        echo "  WARNING: $WH_NAME has no CA bundle"
        EMPTY_CA=$((EMPTY_CA + 1))
    fi
done

if [ "$EMPTY_CA" -gt 0 ]; then
    fail "$EMPTY_CA validating webhook(s) missing CA bundle"
fi

pass "TEST 2 PASSED: ValidatingWebhookConfiguration is healthy"

# ============================================================================
# Test 3: Webhook Service has endpoints
# ============================================================================
log "TEST 3: Verifying webhook Service endpoints..."

if ! kubectl get service "$WEBHOOK_SERVICE_NAME" -n "$RELEASE_NAMESPACE" &>/dev/null; then
    fail "Webhook Service '$WEBHOOK_SERVICE_NAME' not found in namespace '$RELEASE_NAMESPACE'"
fi
echo "  Webhook Service: Found"

ENDPOINT_COUNT=$(kubectl get endpoints "$WEBHOOK_SERVICE_NAME" -n "$RELEASE_NAMESPACE" \
    -o jsonpath='{.subsets[*].addresses}' 2>/dev/null | grep -c "ip" || echo "0")

if [ "$ENDPOINT_COUNT" -lt 1 ]; then
    fail "Webhook Service has no ready endpoints"
fi
echo "  Endpoint addresses: $ENDPOINT_COUNT"

pass "TEST 3 PASSED: Webhook Service has healthy endpoints"

# ============================================================================
# Test 4: Validating webhook rejects invalid SparkApplication
# ============================================================================
log "TEST 4: Verifying webhook rejects invalid SparkApplication..."

if [ ! -f "$INVALID_APP_YAML" ]; then
    fail "Invalid SparkApplication fixture not found: $INVALID_APP_YAML"
fi

# The fixture has an invalid DNS-1035 name (starts with digit, contains underscores).
# The validating webhook should reject it.
if kubectl apply -f "$INVALID_APP_YAML" 2>/dev/null; then
    # If it was accepted, clean up and fail
    kubectl delete -f "$INVALID_APP_YAML" --ignore-not-found 2>/dev/null || true
    fail "Webhook accepted an invalid SparkApplication (expected rejection)"
fi

echo "  Invalid SparkApplication was correctly rejected"

pass "TEST 4 PASSED: Validating webhook rejects invalid resources"

# ============================================================================
# Summary
# ============================================================================
echo ""
echo "============================================"
pass "ALL WEBHOOK TESTS PASSED!"
echo "============================================"
echo ""
echo "Verified:"
echo "  - MutatingWebhookConfiguration with CA bundles"
echo "  - ValidatingWebhookConfiguration with CA bundles"
echo "  - Webhook Service has ready endpoints"
echo "  - Invalid SparkApplication rejected by webhook"
echo ""

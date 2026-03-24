#!/bin/bash
# ============================================================================
# Test: CRD Verification
# ============================================================================
#
# Verifies that the Kustomize-installed Spark Operator CRDs are correctly
# registered and functional in the cluster.
#
# This test verifies:
#   1. All 3 CRDs exist (SparkApplication, ScheduledSparkApplication, SparkConnect)
#   2. Each CRD has the Established condition
#   3. CRDs are served at the expected API versions
#   4. API resources are discoverable via kubectl
#
# Prerequisites:
#   - Spark Operator installed via Kustomize (run test-operator-install.sh first)
#
# Usage:
#   ./test-crds.sh
#
# ============================================================================

set -euo pipefail

# ============================================================================
# Helper Functions
# ============================================================================
log()  { echo "➡️  $1"; }
pass() { echo "✅ $1"; }
fail() { echo "❌ $1"; exit 1; }

EXPECTED_CRDS=(
    "sparkapplications.sparkoperator.k8s.io"
    "scheduledsparkapplications.sparkoperator.k8s.io"
    "sparkconnects.sparkoperator.k8s.io"
)

# ============================================================================
# Test 1: All CRDs exist
# ============================================================================
log "TEST 1: Verifying all Spark Operator CRDs exist..."

for crd in "${EXPECTED_CRDS[@]}"; do
    if kubectl get crd "$crd" &>/dev/null; then
        echo "  Found: $crd"
    else
        fail "CRD not found: $crd"
    fi
done

pass "TEST 1 PASSED: All 3 CRDs exist"

# ============================================================================
# Test 2: CRDs are Established
# ============================================================================
log "TEST 2: Verifying CRDs have Established condition..."

for crd in "${EXPECTED_CRDS[@]}"; do
    CONDITION=$(kubectl get crd "$crd" -o jsonpath='{.status.conditions[?(@.type=="Established")].status}' 2>/dev/null || echo "")
    if [ "$CONDITION" = "True" ]; then
        echo "  $crd: Established=True"
    else
        fail "CRD $crd is not Established (got: '$CONDITION')"
    fi
done

pass "TEST 2 PASSED: All CRDs are Established"

# ============================================================================
# Test 3: CRDs serve expected API versions
# ============================================================================
log "TEST 3: Verifying CRD served versions..."

# SparkApplication and ScheduledSparkApplication should serve v1beta2
for crd in "sparkapplications.sparkoperator.k8s.io" "scheduledsparkapplications.sparkoperator.k8s.io"; do
    VERSIONS=$(kubectl get crd "$crd" -o jsonpath='{.spec.versions[*].name}' 2>/dev/null || echo "")
    if echo "$VERSIONS" | grep -q "v1beta2"; then
        echo "  $crd: serves v1beta2"
    else
        fail "CRD $crd does not serve v1beta2 (versions: $VERSIONS)"
    fi
done

# SparkConnect should serve v1alpha1
VERSIONS=$(kubectl get crd "sparkconnects.sparkoperator.k8s.io" -o jsonpath='{.spec.versions[*].name}' 2>/dev/null || echo "")
if echo "$VERSIONS" | grep -q "v1alpha1"; then
    echo "  sparkconnects.sparkoperator.k8s.io: serves v1alpha1"
else
    fail "CRD sparkconnects.sparkoperator.k8s.io does not serve v1alpha1 (versions: $VERSIONS)"
fi

pass "TEST 3 PASSED: CRDs serve expected versions"

# ============================================================================
# Test 4: API resources are discoverable
# ============================================================================
log "TEST 4: Verifying API resources are discoverable..."

API_RESOURCES=$(kubectl api-resources --api-group=sparkoperator.k8s.io 2>/dev/null || echo "")

for kind in "sparkapplications" "scheduledsparkapplications" "sparkconnects"; do
    if echo "$API_RESOURCES" | grep -qi "$kind"; then
        echo "  Discoverable: $kind"
    else
        fail "API resource not discoverable: $kind"
    fi
done

pass "TEST 4 PASSED: All API resources are discoverable"

# ============================================================================
# Summary
# ============================================================================
echo ""
echo "============================================"
pass "ALL CRD TESTS PASSED!"
echo "============================================"
echo ""
echo "Verified:"
echo "  - 3 CRDs installed and Established"
echo "  - SparkApplication/ScheduledSparkApplication serve v1beta2"
echo "  - SparkConnect serves v1alpha1"
echo "  - All API resources discoverable"
echo ""
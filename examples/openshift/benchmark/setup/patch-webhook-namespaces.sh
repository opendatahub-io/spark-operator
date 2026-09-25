#!/usr/bin/env bash
# Extend Spark Operator Mutating/ValidatingWebhookConfiguration namespaceSelectors
# to include spark-bench-a/b/c (in addition to default).
#
# WARNING: spark-operator-module owns these objects and may re-apply the stock
# selector (default only) on reconcile. Re-run this script before each benchmark
# and verify selectors afterward. Document any revert as a blocker in the report.
set -euo pipefail

NAMESPACES_JSON='["default","spark-bench-a","spark-bench-b","spark-bench-c"]'

patch_webhook() {
  local kind="$1"
  local name="$2"
  echo "Patching ${kind}/${name} namespaceSelectors -> ${NAMESPACES_JSON}"

  # Build a JSON patch that sets namespaceSelector.matchExpressions[0].values for every webhook.
  local count
  count="$(oc get "${kind}" "${name}" -o jsonpath='{.webhooks[*].name}' | wc -w | tr -d ' ')"
  local patches=()
  local i
  for ((i = 0; i < count; i++)); do
    patches+=("{\"op\":\"replace\",\"path\":\"/webhooks/${i}/namespaceSelector\",\"value\":{\"matchExpressions\":[{\"key\":\"kubernetes.io/metadata.name\",\"operator\":\"In\",\"values\":${NAMESPACES_JSON}}]}}")
  done

  local payload
  payload="[$(IFS=,; echo "${patches[*]}")]"
  oc patch "${kind}" "${name}" --type=json -p "${payload}"
}

patch_webhook mutatingwebhookconfiguration mutating-webhook-configuration
patch_webhook validatingwebhookconfiguration validating-webhook-configuration

echo "==> Current Spark Operator webhook namespace selectors:"
oc get mutatingwebhookconfiguration mutating-webhook-configuration -o json | \
  jq -r '.webhooks[] | "\(.name) -> \(.namespaceSelector)"'
oc get validatingwebhookconfiguration validating-webhook-configuration -o json | \
  jq -r '.webhooks[] | select(.clientConfig.service.name=="spark-operator-webhook-svc") | "\(.name) -> \(.namespaceSelector)"'

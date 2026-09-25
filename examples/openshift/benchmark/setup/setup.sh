#!/usr/bin/env bash
# Create benchmark namespaces, RBAC, and optionally extend Spark Operator
# webhook namespace selectors to include the bench namespaces.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAMESPACES=("spark-bench-a" "spark-bench-b" "spark-bench-c")
PATCH_WEBHOOKS="${PATCH_WEBHOOKS:-true}"

echo "==> Applying namespaces"
oc apply -f "${SCRIPT_DIR}/namespaces.yaml"

echo "==> Applying ServiceAccount + Role/RoleBinding per namespace"
for ns in "${NAMESPACES[@]}"; do
  sed "s/NAMESPACE_PLACEHOLDER/${ns}/g" "${SCRIPT_DIR}/rbac.yaml.tmpl" | oc apply -f -
done

echo "==> Granting anyuid SCC to spark SA (apache/spark image requires UID 185)"
for ns in "${NAMESPACES[@]}"; do
  oc adm policy add-scc-to-user anyuid -z spark -n "${ns}"
done

if [[ "${PATCH_WEBHOOKS}" == "true" ]]; then
  echo "==> Extending Spark Operator webhook namespace selectors"
  "${SCRIPT_DIR}/patch-webhook-namespaces.sh"
else
  echo "==> Skipping webhook patch (PATCH_WEBHOOKS=${PATCH_WEBHOOKS})"
  echo "    NOTE: On this RHOAI cluster webhooks currently match only namespace 'default'."
fi

echo "==> Verify"
for ns in "${NAMESPACES[@]}"; do
  oc get sa spark -n "${ns}"
  oc get rolebinding spark-role-binding -n "${ns}"
done

echo "Setup complete. Next: ./run.sh (or smoke-test commands in README.md)"

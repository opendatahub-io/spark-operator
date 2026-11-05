#!/usr/bin/env bash
# Create benchmark namespaces, RBAC, and anyuid for the spark ServiceAccount.
# Webhook scope is set by run.sh via SparkOperator spec.spark.jobNamespaces.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAMESPACES=("spark-bench-a" "spark-bench-b" "spark-bench-c")

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

echo "==> Verify"
for ns in "${NAMESPACES[@]}"; do
  oc get sa spark -n "${ns}"
  oc get rolebinding spark-role-binding -n "${ns}"
done

echo "Setup complete. Next: ./run.sh (or smoke-test commands in README.md)"

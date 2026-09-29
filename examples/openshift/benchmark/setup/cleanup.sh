#!/usr/bin/env bash
# Remove benchmark SparkApplications and optionally namespaces.
set -euo pipefail

NAMESPACES=("spark-bench-a" "spark-bench-b" "spark-bench-c")
DELETE_NAMESPACES="${DELETE_NAMESPACES:-false}"
RUN_ID="${RUN_ID:-}"

echo "==> Deleting SparkApplications"
for ns in "${NAMESPACES[@]}"; do
  if ! oc get ns "${ns}" >/dev/null 2>&1; then
    echo "Namespace ${ns} not found; skipping"
    continue
  fi
  if [[ -n "${RUN_ID}" ]]; then
    echo "Deleting apps with spark-bench.run-id=${RUN_ID} in ${ns}"
    oc delete sparkapplication -n "${ns}" -l "spark-bench.run-id=${RUN_ID}" --ignore-not-found=true
  else
    echo "Deleting all spark-bench labeled apps in ${ns}"
    oc delete sparkapplication -n "${ns}" -l spark-bench=true --ignore-not-found=true
  fi
done

if [[ "${DELETE_NAMESPACES}" == "true" ]]; then
  echo "==> Deleting namespaces"
  for ns in "${NAMESPACES[@]}"; do
    oc delete ns "${ns}" --ignore-not-found=true
  done
else
  echo "Namespaces retained (set DELETE_NAMESPACES=true to remove)."
fi

echo "Cleanup complete."

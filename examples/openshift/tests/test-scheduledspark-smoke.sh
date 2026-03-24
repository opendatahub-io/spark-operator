#!/usr/bin/env bash
set -euo pipefail

# ScheduledSparkApplication smoke test (no RBAC checks here)
# Env (override as needed):
#   NAMESPACE (default: redhat-ods-applications)
#   SCHED_NAME (default: rbac-scheduled-smoke)
#   TIMEOUT_SECONDS (default: 180)
#   SPARK_IMAGE (default: quay.io/ssankepe/spark-openshift:3.5.7)
#   APP_YAML (default: script_dir/manifests/scheduledspark-smoke-app.yaml)

NAMESPACE="${NAMESPACE:-redhat-ods-applications}"
SCHED_NAME="${SCHED_NAME:-rbac-scheduled-smoke}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-180}"
SPARK_IMAGE="${SPARK_IMAGE:-quay.io/ssankepe/spark-openshift:3.5.7}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_YAML="${APP_YAML:-$SCRIPT_DIR/manifests/scheduledspark-smoke-app.yaml}"

apply_sched() {
  if [[ ! -f "$APP_YAML" ]]; then
    echo "ScheduledSparkApplication YAML not found: $APP_YAML" >&2
    exit 1
  fi
  export NAMESPACE SCHED_NAME SPARK_IMAGE
  envsubst < "$APP_YAML" | oc apply -f -
}

cleanup() {
  oc delete scheduledsparkapplication "${SCHED_NAME}" -n "${NAMESPACE}" --ignore-not-found || true
}
trap cleanup EXIT

echo "Apply ScheduledSparkApplication ${SCHED_NAME}"
apply_sched

echo "Wait for child SparkApplication (<= ${TIMEOUT_SECONDS}s)"
start=$(date +%s)
while true; do
  child="$(oc get sparkapplications -n "${NAMESPACE}" \
    -l sparkoperator.k8s.io/launched-by-scheduled-sparkapplication-name="${SCHED_NAME}" \
    -o jsonpath='{.items[-1:].metadata.name}' 2>/dev/null || true)"
  if [[ -n "$child" ]]; then
    echo "Spawned: $child"
    break
  fi
  (( $(date +%s) - start > TIMEOUT_SECONDS )) && {
    echo "Timed out after ${TIMEOUT_SECONDS}s"
    oc describe scheduledsparkapplication "${SCHED_NAME}" -n "${NAMESPACE}" || true
    oc logs deploy/spark-operator-controller -n "${NAMESPACE}" --since=5m || true
    exit 1
  }
  sleep 5
done

oc get scheduledsparkapplication "${SCHED_NAME}" -n "${NAMESPACE}" \
  -o jsonpath='{.status.lastRun}{" "}{.status.lastRunName}{" "}{.status.scheduleState}{"\n"}' || true
oc get sparkapplication "$child" -n "${NAMESPACE}" -o wide || true
echo "Smoke test succeeded"


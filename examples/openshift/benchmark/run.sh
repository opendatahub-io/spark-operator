#!/usr/bin/env bash
# Entry point for the OpenShift AI Spark Operator P0 validation benchmark.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS="${ROOT}/harness"
RESULTS_BASE="${ROOT}/results"
RUN_ID="${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
RESULTS_DIR="${RESULTS_BASE}/${RUN_ID}"
mkdir -p "${RESULTS_DIR}"

USERS="${USERS:-3}"
JOBS_PER_USER="${JOBS_PER_USER:-17}"
JOBS_PER_MIN="${JOBS_PER_MIN:-30}"
NAMESPACES="${NAMESPACES:-spark-bench-a,spark-bench-b,spark-bench-c}"
TEMPLATE="${TEMPLATE:-${ROOT}/workloads/spark-app-openshift.yaml}"
SERVICE_ACCOUNT="${SERVICE_ACCOUNT:-spark}"
NO_CLEANUP="${NO_CLEANUP:-}"
# ROSA API TLS often fails under Python's cert store even when `oc` works.
export SPARK_BENCH_INSECURE_SKIP_TLS_VERIFY="${SPARK_BENCH_INSECURE_SKIP_TLS_VERIFY:-true}"

echo "==> Recording versions -> ${RESULTS_DIR}/versions.json"
python3 "${HARNESS}/record_versions.py" "${RESULTS_DIR}/versions.json" || true

echo "==> Re-applying webhook namespace selectors (module may have reverted them)"
PATCH_OK=false
for attempt in 1 2 3 4 5; do
  "${ROOT}/setup/patch-webhook-namespaces.sh" | tee -a "${RESULTS_DIR}/webhook-patch.log"
  sleep 2
  if oc get mutatingwebhookconfiguration mutating-webhook-configuration -o json | \
    jq -e '
      [.webhooks[]
       | select(.name|test("mutate-sparkapplication|mutate-pod"))
       | .namespaceSelector.matchExpressions[]?
       | select(.key=="kubernetes.io/metadata.name")
       | .values[]?]
      | index("spark-bench-a") != null
    ' >/dev/null; then
    PATCH_OK=true
    break
  fi
  echo "Webhook selectors reverted (attempt ${attempt}/5); retrying patch..."
done

oc get mutatingwebhookconfiguration mutating-webhook-configuration -o json | \
  jq -r '.webhooks[] | select(.name|test("sparkapplication|pod")) | "\(.name): \(.namespaceSelector)"' \
  | tee "${RESULTS_DIR}/webhook-selectors.txt"

if [[ "${PATCH_OK}" != "true" ]]; then
  if [[ "${ALLOW_WEBHOOK_REVERT:-}" == "1" ]]; then
    echo "WARNING: Spark Operator webhooks only cover 'default' (module reconcile)."
    echo "         Continuing because ALLOW_WEBHOOK_REVERT=1 — document as blocker."
  else
    echo "ERROR: webhook namespace selectors do not retain spark-bench-* after patch." >&2
    echo "       spark-operator-module reconciles them back to ['default'] only." >&2
    echo "       Re-run with ALLOW_WEBHOOK_REVERT=1 to continue and record the blocker," >&2
    echo "       or ask Platform how to configure webhook watched namespaces durably." >&2
    exit 1
  fi
fi

if [[ ! -d "${HARNESS}/.venv" ]]; then
  echo "==> Creating venv and installing harness"
  python3 -m venv "${HARNESS}/.venv"
  # shellcheck disable=SC1091
  source "${HARNESS}/.venv/bin/activate"
  pip install -U pip
  pip install -r "${HARNESS}/requirements.txt"
else
  # shellcheck disable=SC1091
  source "${HARNESS}/.venv/bin/activate"
fi

EXTRA_ARGS=()
if [[ -n "${NO_CLEANUP}" ]]; then
  EXTRA_ARGS+=(--no-spark-cleanup)
fi

echo "==> Running Locust (run_id=${RUN_ID}, TLS_SKIP=${SPARK_BENCH_INSECURE_SKIP_TLS_VERIFY})"
cd "${HARNESS}"
locust --headless --only-summary \
  -u "${USERS}" -r 1 \
  --run-time 2h \
  --job-limit-per-user "${JOBS_PER_USER}" \
  --jobs-per-min "${JOBS_PER_MIN}" \
  --spark-namespaces "${NAMESPACES}" \
  --spark-template "${TEMPLATE}" \
  --spark-service-account "${SERVICE_ACCOUNT}" \
  --run-id "${RUN_ID}" \
  --results-dir "${RESULTS_BASE}" \
  "${EXTRA_ARGS[@]}" \
  2>&1 | tee "${RESULTS_DIR}/run.log"

echo "==> Results in ${RESULTS_DIR}"
ls -la "${RESULTS_DIR}"

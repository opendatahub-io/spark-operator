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

# The module owns the webhook objects and copies namespaceSelector from
# spec.spark.jobNamespaces. Patching the webhook configuration directly does not stick.
echo "==> Merging benchmark namespaces into SparkOperator spec.spark.jobNamespaces"
current="$(oc get sparkoperator default-sparkoperator -o json | jq -c '.spec.spark.jobNamespaces // []')"
merged="$(jq -nc --argjson current "${current}" --arg ns "${NAMESPACES}" '
  ($ns | split(",") | map(gsub("^\\s+|\\s+$"; "")) | map(select(length > 0))) as $want
  | ($current + $want) | unique
')"
patch="$(jq -nc --argjson namespaces "${merged}" '{spec: {spark: {jobNamespaces: $namespaces}}}')"
oc patch sparkoperator default-sparkoperator --type=merge -p "${patch}" | tee "${RESULTS_DIR}/webhook-patch.log"
echo "jobNamespaces: ${merged}" | tee -a "${RESULTS_DIR}/webhook-patch.log"

echo "==> Waiting for webhook namespaceSelectors to include ${NAMESPACES}"
PATCH_OK=false
IFS=',' read -r -a WANT_NAMESPACES <<< "${NAMESPACES}"
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  selector_values="$(oc get mutatingwebhookconfiguration mutating-webhook-configuration -o json | jq -r '
    [.webhooks[]
     | select(.name | test("mutate-sparkapplication"))
     | .namespaceSelector.matchExpressions[]?
     | select(.key == "kubernetes.io/metadata.name")
     | .values[]]
    | unique
    | .[]
  ')"
  missing=0
  for ns in "${WANT_NAMESPACES[@]}"; do
    ns="${ns#"${ns%%[![:space:]]*}"}"
    ns="${ns%"${ns##*[![:space:]]}"}"
    [[ -z "${ns}" ]] && continue
    if ! grep -qx "${ns}" <<< "${selector_values}"; then
      missing=1
      break
    fi
  done
  if [[ "${missing}" -eq 0 ]]; then
    PATCH_OK=true
    break
  fi
  echo "Webhook selectors do not yet include all benchmark namespaces (attempt ${attempt}/10)..."
  sleep 3
done

oc get mutatingwebhookconfiguration mutating-webhook-configuration -o json |
  jq -r '.webhooks[] | select(.name|test("sparkapplication|pod")) | "\(.name): \(.namespaceSelector)"' |
  tee "${RESULTS_DIR}/webhook-selectors.txt"

if [[ "${PATCH_OK}" != "true" ]]; then
  if [[ "${ALLOW_WEBHOOK_REVERT:-}" == "1" ]]; then
    echo "WARNING: webhook namespaceSelectors do not include ${NAMESPACES}."
    echo "         Continuing because ALLOW_WEBHOOK_REVERT=1 — document as blocker."
  else
    echo "ERROR: webhook namespaceSelectors do not include ${NAMESPACES}." >&2
    echo "       The module sets them from SparkOperator spec.spark.jobNamespaces." >&2
    echo "       Re-run with ALLOW_WEBHOOK_REVERT=1 to continue and record the blocker." >&2
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

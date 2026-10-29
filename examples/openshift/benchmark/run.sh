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

echo "==> Webhook inventory -> ${RESULTS_DIR}/webhook-inventory.json"
python3 "${HARNESS}/webhook_inventory.py" "${RESULTS_DIR}/webhook-inventory.json" || true

echo "==> Capacity snapshot -> ${RESULTS_DIR}/capacity.json"
python3 "${HARNESS}/capacity_snapshot.py" "${RESULTS_DIR}/capacity.json" "${NAMESPACES}" || true

# webhook scope durable through SparkOperator.spec.spark.jobNamespaces.
echo "==> Merging benchmark namespaces into SparkOperator spec.spark.jobNamespaces"
current="$(oc get sparkoperator default-sparkoperator -o json | jq -c '.spec.spark.jobNamespaces // []')"
merged="$(jq -nc --argjson current "${current}" --arg ns "${NAMESPACES}" '
  ($ns | split(",") | map(gsub("^\\s+|\\s+$"; "")) | map(select(length > 0))) as $want
  | ($current + $want) | unique
')"
patch="$(jq -nc --argjson namespaces "${merged}" '{spec: {spark: {jobNamespaces: $namespaces}}}')"
oc patch sparkoperator default-sparkoperator --type=merge -p "${patch}" |
  tee "${RESULTS_DIR}/webhook-patch.log"
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
    echo "       Verify the SparkOperator CRD and module controller are healthy." >&2
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

# controller resources durable through SparkOperator.spec.spark.controllerResources.
resources_requested=false
if [[ -n "${CONTROLLER_CPU_REQUEST:-}" || -n "${CONTROLLER_MEMORY_REQUEST:-}" ||
  -n "${CONTROLLER_CPU_LIMIT:-}" || -n "${CONTROLLER_MEMORY_LIMIT:-}" ]]; then
  resources_requested=true
  cpu_req="${CONTROLLER_CPU_REQUEST:-500m}"
  mem_req="${CONTROLLER_MEMORY_REQUEST:-2Gi}"
  cpu_lim="${CONTROLLER_CPU_LIMIT:-2}"
  mem_lim="${CONTROLLER_MEMORY_LIMIT:-8Gi}"

  echo "==> Patching SparkOperator controllerResources (cpu=${cpu_req}/${cpu_lim}, mem=${mem_req}/${mem_lim})"
  oc patch sparkoperator default-sparkoperator --type=merge -p "$(jq -nc \
    --arg cpu_req "${cpu_req}" --arg cpu_lim "${cpu_lim}" \
    --arg mem_req "${mem_req}" --arg mem_lim "${mem_lim}" \
    '{spec:{spark:{controllerResources:{
      requests:{cpu:$cpu_req,memory:$mem_req},
      limits:{cpu:$cpu_lim,memory:$mem_lim}
    }}}}')" | tee -a "${RESULTS_DIR}/operator-patch.log"

  echo "==> Waiting for controller resources to reach the Deployment"
  resources_ready=false
  for attempt in $(seq 1 120); do
    if oc get deployment spark-operator-controller -n redhat-ods-applications -o json |
      jq -e \
        --arg cpu_req "${cpu_req}" --arg cpu_lim "${cpu_lim}" \
        --arg mem_req "${mem_req}" --arg mem_lim "${mem_lim}" '
          .spec.template.spec.containers[]
          | select(.name == "controller")
          | .resources.requests.cpu == $cpu_req
            and .resources.requests.memory == $mem_req
            and .resources.limits.cpu == $cpu_lim
            and .resources.limits.memory == $mem_lim
        ' > /dev/null; then
      resources_ready=true
      break
    fi
    sleep 5
  done
  if [[ "${resources_ready}" != "true" ]]; then
    echo "ERROR: controller resources did not reconcile within 10 minutes." >&2
    exit 1
  fi
  oc rollout status deployment/spark-operator-controller \
    -n redhat-ods-applications --timeout=10m
fi

expected_controller_args=()
[[ -n "${CONTROLLER_THREADS:-}" ]] && expected_controller_args+=("--controller-threads=${CONTROLLER_THREADS}")
[[ -n "${CONTROLLER_WORKQUEUE_QPS:-}" ]] && expected_controller_args+=("--workqueue-ratelimiter-bucket-qps=${CONTROLLER_WORKQUEUE_QPS}")
[[ -n "${CONTROLLER_WORKQUEUE_BURST:-}" ]] && expected_controller_args+=("--workqueue-ratelimiter-bucket-size=${CONTROLLER_WORKQUEUE_BURST}")
[[ -n "${CONTROLLER_WORKQUEUE_MAX_DELAY:-}" ]] && expected_controller_args+=("--workqueue-ratelimiter-max-delay=${CONTROLLER_WORKQUEUE_MAX_DELAY}")
[[ -n "${CONTROLLER_KUBE_API_QPS:-}" ]] && expected_controller_args+=("--kube-api-qps=${CONTROLLER_KUBE_API_QPS}")
[[ -n "${CONTROLLER_KUBE_API_BURST:-}" ]] && expected_controller_args+=("--kube-api-burst=${CONTROLLER_KUBE_API_BURST}")

if [[ "${#expected_controller_args[@]}" -gt 0 ]]; then
  echo "==> Patching SparkOperator controllerConfiguration"
  {
    printf '%s\n' "${expected_controller_args[@]}"
    controller_configuration_patch="$(jq -nc \
      --arg threads "${CONTROLLER_THREADS:-}" \
      --arg workqueue_qps "${CONTROLLER_WORKQUEUE_QPS:-}" \
      --arg workqueue_burst "${CONTROLLER_WORKQUEUE_BURST:-}" \
      --arg workqueue_max_delay "${CONTROLLER_WORKQUEUE_MAX_DELAY:-}" \
      --arg kube_api_qps "${CONTROLLER_KUBE_API_QPS:-}" \
      --arg kube_api_burst "${CONTROLLER_KUBE_API_BURST:-}" '
          def number_field($name; $value):
            if $value == "" then {} else {($name): ($value | tonumber)} end;
          def string_field($name; $value):
            if $value == "" then {} else {($name): $value} end;

          {spec: {spark: {controllerConfiguration:
            (number_field("workers"; $threads)
            + number_field("kubeAPIQPS"; $kube_api_qps)
            + number_field("kubeAPIBurst"; $kube_api_burst)
            + (if ($workqueue_qps != "" or $workqueue_burst != "" or $workqueue_max_delay != "")
               then {workqueueRateLimiter:
                 (number_field("bucketQPS"; $workqueue_qps)
                 + number_field("bucketSize"; $workqueue_burst)
                 + string_field("maxDelay"; $workqueue_max_delay))}
               else {} end))
          }}}
        ')"
    oc patch sparkoperator default-sparkoperator --type=merge \
      -p "${controller_configuration_patch}"
  } | tee -a "${RESULTS_DIR}/operator-patch.log"

  echo "==> Waiting for controller configuration to reach the Deployment"
  controller_configuration_ready=false
  for attempt in $(seq 1 120); do
    actual_controller_args="$(oc get deployment spark-operator-controller \
      -n redhat-ods-applications -o json 2> /dev/null |
      jq -r '.spec.template.spec.containers[] | select(.name == "controller") | .args[]')" || true
    controller_configuration_ready=true
    for expected_arg in "${expected_controller_args[@]}"; do
      if ! grep -Fqx -- "${expected_arg}" <<< "${actual_controller_args}"; then
        controller_configuration_ready=false
        break
      fi
    done
    if [[ "${controller_configuration_ready}" == "true" ]]; then
      break
    fi
    sleep 5
  done
  if [[ "${controller_configuration_ready}" != "true" ]]; then
    echo "ERROR: controller configuration did not reconcile within 10 minutes." >&2
    exit 1
  fi
  oc rollout status deployment/spark-operator-controller \
    -n redhat-ods-applications --timeout=10m
fi

# Start every measured cell with a fresh controller process so cumulative
# operator counters do not leak across experiments.
if [[ "${RESET_CONTROLLER_BEFORE_RUN:-true}" == "true" ]]; then
  echo "==> Restarting the controller before the workload"
  oc rollout restart deployment/spark-operator-controller -n redhat-ods-applications
  oc rollout status deployment/spark-operator-controller \
    -n redhat-ods-applications --timeout=10m
fi

if [[ "${resources_requested}" == "true" ]]; then
  oc get deployment spark-operator-controller -n redhat-ods-applications -o json |
    jq -e \
      --arg cpu_req "${cpu_req}" --arg cpu_lim "${cpu_lim}" \
      --arg mem_req "${mem_req}" --arg mem_lim "${mem_lim}" '
        .spec.template.spec.containers[]
        | select(.name == "controller")
        | .resources.requests.cpu == $cpu_req
          and .resources.requests.memory == $mem_req
          and .resources.limits.cpu == $cpu_lim
          and .resources.limits.memory == $mem_lim
      ' > /dev/null || {
    echo "ERROR: live controller resources do not match the requested values." >&2
    exit 1
  }
fi

if [[ "${#expected_controller_args[@]}" -gt 0 ]]; then
  actual_controller_args="$(oc get deployment spark-operator-controller \
    -n redhat-ods-applications -o json |
    jq -r '.spec.template.spec.containers[] | select(.name == "controller") | .args[]')"
  for expected_arg in "${expected_controller_args[@]}"; do
    if ! grep -Fqx -- "${expected_arg}" <<< "${actual_controller_args}"; then
      echo "ERROR: live controller is missing requested argument ${expected_arg}." >&2
      exit 1
    fi
  done
fi

record_controller_state() {
  jq -n \
    --argjson deployment "$(
      oc get deployment spark-operator-controller -n redhat-ods-applications -o json |
        jq '
          .spec.template.spec.containers[]
          | select(.name == "controller")
          | {
              image,
              resources,
              args: [
                .args[]
                | select(test("^--(controller-threads|workqueue-ratelimiter|kube-api-)"))
              ]
            }
        '
    )" \
    --argjson pods "$(
      oc get pods -n redhat-ods-applications \
        -l 'app.kubernetes.io/name=spark-operator,app.kubernetes.io/component=controller' \
        -o json |
        jq '[
          .items[]
          | select(.metadata.deletionTimestamp == null)
          | {
              name: .metadata.name,
              uid: .metadata.uid,
              phase: .status.phase,
              restartCount: ([.status.containerStatuses[]?.restartCount] | add // 0)
            }
        ] | sort_by(.name)'
    )" \
    '{deployment: $deployment, pods: $pods}'
}

record_controller_state > "${RESULTS_DIR}/controller-state-before.json"

EXTRA_ARGS=()
if [[ -n "${NO_CLEANUP}" ]]; then
  EXTRA_ARGS+=(--no-spark-cleanup)
fi

echo "==> Running Locust (run_id=${RUN_ID}, TLS_SKIP=${SPARK_BENCH_INSECURE_SKIP_TLS_VERIFY})"
cd "${HARNESS}"
set +e
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
locust_status="${PIPESTATUS[0]}"
set -e

record_controller_state > "${RESULTS_DIR}/controller-state-after.json"
configuration_stable=true
if ! diff -u \
  "${RESULTS_DIR}/controller-state-before.json" \
  "${RESULTS_DIR}/controller-state-after.json" |
  tee "${RESULTS_DIR}/controller-state.diff"; then
  configuration_stable=false
fi

# Refresh capacity.json after the run so SCC/NP event scans cover the load window.
echo "==> Refreshing capacity snapshot (post-run admission events) -> ${RESULTS_DIR}/capacity.json"
python3 "${HARNESS}/capacity_snapshot.py" "${RESULTS_DIR}/capacity.json" "${NAMESPACES}" || true

echo "==> Results in ${RESULTS_DIR}"
ls -la "${RESULTS_DIR}"

if [[ "${configuration_stable}" != "true" ]]; then
  echo "ERROR: controller configuration or pod identity changed during the benchmark; discard this run." >&2
  exit 1
fi

exit "${locust_status}"

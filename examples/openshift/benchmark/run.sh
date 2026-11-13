#!/usr/bin/env bash
# Entry point for the OpenShift AI Spark Operator P0 validation benchmark.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS="${ROOT}/harness"
"${HARNESS}/setup_venv.sh"

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
# Verify TLS by default. If Python cannot validate the ROSA certificate chain,
# fix the local trust store or explicitly set this escape hatch to true.
export SPARK_BENCH_INSECURE_SKIP_TLS_VERIFY="${SPARK_BENCH_INSECURE_SKIP_TLS_VERIFY:-false}"
WEBHOOK_WAIT_TIMEOUT_SECONDS="${WEBHOOK_WAIT_TIMEOUT_SECONDS:-600}"
WEBHOOK_WAIT_INTERVAL_SECONDS="${WEBHOOK_WAIT_INTERVAL_SECONDS:-5}"
CONTROLLER_START_LATENCY_BUCKETS="${CONTROLLER_START_LATENCY_BUCKETS:-30,60,90,120,150,180,210,240,270,300,400,500,600}"

for value_name in USERS JOBS_PER_USER WEBHOOK_WAIT_TIMEOUT_SECONDS WEBHOOK_WAIT_INTERVAL_SECONDS; do
  value="${!value_name}"
  if [[ ! "${value}" =~ ^[1-9][0-9]*$ ]]; then
    echo "ERROR: ${value_name} must be a positive integer; got '${value}'." >&2
    exit 1
  fi
done
TOTAL_APPLICATIONS=$((USERS * JOBS_PER_USER))

PYTHON_CMD="${HARNESS}/.venv/bin/python"
# shellcheck disable=SC1091
source "${HARNESS}/.venv/bin/activate"

echo "==> Recording versions -> ${RESULTS_DIR}/versions.json"
if ! "${PYTHON_CMD}" "${HARNESS}/record_versions.py" "${RESULTS_DIR}/versions.json"; then
  echo "WARNING: failed to record component versions; continuing with an incomplete artifact set." >&2
fi

echo "==> Webhook inventory -> ${RESULTS_DIR}/webhook-inventory.json"
if ! "${PYTHON_CMD}" "${HARNESS}/webhook_inventory.py" "${RESULTS_DIR}/webhook-inventory.json"; then
  echo "WARNING: failed to record the webhook inventory; continuing with an incomplete artifact set." >&2
fi

echo "==> Capacity snapshot -> ${RESULTS_DIR}/capacity.json"
capacity_args=(
  "${RESULTS_DIR}/capacity.json"
  "${NAMESPACES}"
  --template "${TEMPLATE}"
  --applications "${TOTAL_APPLICATIONS}"
)
if [[ "${ACK_LOW_CAPACITY:-}" == "1" ]]; then
  capacity_args+=(--ack-low-capacity)
fi
if ! "${PYTHON_CMD}" "${HARNESS}/capacity_snapshot.py" "${capacity_args[@]}"; then
  echo "ERROR: failed to capture cluster capacity; refusing to start an unqualified benchmark run." >&2
  exit 1
fi

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
webhook_max_attempts=$(((WEBHOOK_WAIT_TIMEOUT_SECONDS + WEBHOOK_WAIT_INTERVAL_SECONDS - 1) / WEBHOOK_WAIT_INTERVAL_SECONDS))
for ((attempt = 1; attempt <= webhook_max_attempts; attempt++)); do
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
  echo "Webhook selectors do not yet include all benchmark namespaces (attempt ${attempt}/${webhook_max_attempts})..."
  if ((attempt < webhook_max_attempts)); then
    sleep "${WEBHOOK_WAIT_INTERVAL_SECONDS}"
  fi
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
expected_controller_args+=("--metrics-job-start-latency-buckets=${CONTROLLER_START_LATENCY_BUCKETS}")

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
      --arg kube_api_burst "${CONTROLLER_KUBE_API_BURST:-}" \
      --arg start_latency_buckets "${CONTROLLER_START_LATENCY_BUCKETS}" '
          def number_field($name; $value):
            if $value == "" then {} else {($name): ($value | tonumber)} end;

          {spec: {spark: {controllerConfiguration:
            (number_field("workers"; $threads)
            + number_field("kubeAPIQPS"; $kube_api_qps)
            + number_field("kubeAPIBurst"; $kube_api_burst)
            + {metricsJobStartLatencyBuckets:
                ($start_latency_buckets | split(",") | map(tonumber))}
            + (if ($workqueue_qps != "" or $workqueue_burst != "" or $workqueue_max_delay != "")
               then {workqueueRateLimiter:
                 (number_field("bucketQPS"; $workqueue_qps)
                 + number_field("bucketSize"; $workqueue_burst)
                 + (if $workqueue_max_delay == ""
                    then {}
                    else {maxDelay: {enabled: true, duration: $workqueue_max_delay}}
                    end))}
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

warm_workload_image() {
  local image pull_policy namespace daemonset manifest
  image="$(jq -er '.workload.image | select(type == "string" and length > 0)' \
    "${RESULTS_DIR}/capacity.json")"
  pull_policy="$(jq -er '.workload.image_pull_policy' "${RESULTS_DIR}/capacity.json")"
  namespace="${NAMESPACES%%,*}"
  namespace="${namespace#"${namespace%%[![:space:]]*}"}"
  namespace="${namespace%"${namespace##*[![:space:]]}"}"
  daemonset="spark-benchmark-image-warmup"

  echo "==> Pre-pulling ${image} on every eligible worker node"
  oc delete daemonset "${daemonset}" -n "${namespace}" \
    --ignore-not-found --wait=true > /dev/null
  manifest="$(jq -nc \
    --arg name "${daemonset}" \
    --arg namespace "${namespace}" \
    --arg image "${image}" \
    --arg pull_policy "${pull_policy}" \
    --arg service_account "${SERVICE_ACCOUNT}" '
      {
        apiVersion: "apps/v1",
        kind: "DaemonSet",
        metadata: {name: $name, namespace: $namespace},
        spec: {
          selector: {matchLabels: {"spark-bench.component": "image-warmup"}},
          template: {
            metadata: {labels: {"spark-bench.component": "image-warmup"}},
            spec: {
              serviceAccountName: $service_account,
              containers: [{
                name: "image-cache",
                image: $image,
                imagePullPolicy: $pull_policy,
                command: ["/bin/sh", "-c", "sleep 3600"],
                resources: {requests: {cpu: "1m", memory: "1Mi"}},
                securityContext: {
                  allowPrivilegeEscalation: false,
                  capabilities: {drop: ["ALL"]},
                  runAsUser: 185,
                  runAsGroup: 185,
                  runAsNonRoot: true
                }
              }]
            }
          }
        }
      }
    ')"
  printf '%s\n' "${manifest}" | oc apply -f - > /dev/null

  if ! oc rollout status "daemonset/${daemonset}" -n "${namespace}" --timeout=15m ||
    ! oc get daemonset "${daemonset}" -n "${namespace}" -o json | jq -e '
      .status.desiredNumberScheduled > 0
      and .status.numberReady == .status.desiredNumberScheduled
    ' > /dev/null; then
    oc delete daemonset "${daemonset}" -n "${namespace}" --wait=false > /dev/null 2>&1 || true
    echo "ERROR: failed to pre-pull ${image} on every eligible worker node." >&2
    exit 1
  fi

  oc delete daemonset "${daemonset}" -n "${namespace}" --wait=true > /dev/null
  echo "Image warm-up complete; all eligible worker nodes have ${image} cached."
}

warm_workload_image

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
                | select(test("^--(controller-threads|workqueue-ratelimiter|kube-api-|metrics-job-start-latency-buckets)"))
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

locust_pid=""
# Called indirectly by the INT and TERM traps below.
# shellcheck disable=SC2329
abort_run() {
  local signal="$1"
  local exit_code="$2"

  trap - INT TERM
  echo "WARNING: benchmark interrupted by ${signal}; preserving partial artifacts." >&2

  if [[ -n "${locust_pid}" ]] && kill -0 "${locust_pid}" 2> /dev/null; then
    kill -TERM "${locust_pid}" 2> /dev/null || true
    wait "${locust_pid}" 2> /dev/null || true
  fi

  if ! record_controller_state > "${RESULTS_DIR}/controller-state-after.json"; then
    echo "WARNING: failed to capture controller state after interruption." >&2
  fi

  {
    echo "run_id=${RUN_ID}"
    echo "signal=${signal}"
    echo "aborted_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  } > "${RESULTS_DIR}/aborted.txt"

  exit "${exit_code}"
}

trap 'abort_run SIGINT 130' INT
trap 'abort_run SIGTERM 143' TERM

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
  > >(tee "${RESULTS_DIR}/run.log") 2>&1 &
locust_pid="$!"
wait "${locust_pid}"
locust_status="$?"
locust_pid=""
set -e
trap - INT TERM

record_controller_state > "${RESULTS_DIR}/controller-state-after.json"
configuration_stable=true
if ! diff -u \
  "${RESULTS_DIR}/controller-state-before.json" \
  "${RESULTS_DIR}/controller-state-after.json" |
  tee "${RESULTS_DIR}/controller-state.diff"; then
  configuration_stable=false
fi

metrics_snapshot_ok=true
if [[ "${SKIP_METRICS_SNAPSHOT:-}" == "1" ]]; then
  echo "WARNING: skipping the Prometheus snapshot because SKIP_METRICS_SNAPSHOT=1." >&2
elif ! "${ROOT}/metrics/snapshot.sh" "${RESULTS_DIR}"; then
  metrics_snapshot_ok=false
  echo "ERROR: failed to preserve the post-run Prometheus metrics." >&2
fi

# Preserve the qualified pre-run snapshot and record post-run admission events separately.
echo "==> Post-run capacity snapshot -> ${RESULTS_DIR}/capacity-after.json"
if ! "${PYTHON_CMD}" "${HARNESS}/capacity_snapshot.py" \
  "${RESULTS_DIR}/capacity-after.json" "${NAMESPACES}"; then
  echo "WARNING: failed to refresh the post-run capacity snapshot; preserving the completed run." >&2
fi

echo "==> Results in ${RESULTS_DIR}"
ls -la "${RESULTS_DIR}"

if [[ "${configuration_stable}" != "true" ]]; then
  echo "ERROR: controller configuration or pod identity changed during the benchmark; discard this run." >&2
  exit 1
fi
if [[ "${metrics_snapshot_ok}" != "true" ]]; then
  exit 1
fi

exit "${locust_status}"

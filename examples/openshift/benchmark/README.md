# OpenShift AI Spark Operator benchmark

This directory contains a reduced-scale benchmark for measuring Spark Operator
submission and application-start behavior on Red Hat OpenShift AI (RHOAI). It
adapts the [upstream Spark Operator benchmark][upstream-benchmark] to submit
`SparkApplication` resources from an external host, collect lifecycle results,
capture Prometheus evidence from the OpenShift Thanos querier, and verify that
the controller configuration remains stable during a run.

The benchmark measures operator, admission, Kubernetes API, scheduling, and pod
start behavior. It does not measure Spark computation performance.

## Prerequisites

- A running RHOAI cluster with the Spark Operator component in the `Managed`
  state.
- `cluster-admin` access. The setup grants the Apache Spark UID access to the
  `anyuid` SCC and the metrics collector reads OpenShift monitoring resources.
- `oc`, `jq`, `sed`, and Bash on the load-generator host.
- Python 3.11 or 3.12. `run.sh` creates `harness/.venv` and installs the pinned
  Python dependencies automatically.
- Network access from the load-generator host to the OpenShift API and Thanos
  route, and from cluster nodes to the workload image registry.
- A `SparkOperator` CRD that supports `spec.spark.jobNamespaces`,
  `spec.spark.controllerResources`, and `spec.spark.controllerConfiguration`,
  including `metricsJobStartLatencyBuckets`.

Run all commands below from `examples/openshift/benchmark`.

Verify the target cluster before preparing it:

```bash
oc whoami
oc get sparkoperator default-sparkoperator
oc get pods -n redhat-ods-applications | grep spark-operator
oc get route thanos-querier -n openshift-monitoring
```

## Prepare the benchmark environment

```bash
./setup/setup.sh
```

The setup creates `spark-bench-a`, `spark-bench-b`, and `spark-bench-c`, then
adds the `spark` ServiceAccount and its RBAC and SCC permissions in each
namespace. It does not start a benchmark run.

To validate the environment with one application before a full run:

```bash
RUN_ID="smoke-$(date -u +%Y%m%dT%H%M%SZ)"
RUN_ID="$RUN_ID" NO_CLEANUP=1 \
  USERS=1 JOBS_PER_USER=1 JOBS_PER_MIN=1 \
  NAMESPACES=spark-bench-a \
  ./run.sh
```

## Run the 51-application baseline

The baseline submits 51 applications across three namespaces. Each application
uses one driver and five static executors from
`workloads/spark-app-openshift.yaml`.

```bash
RUN_ID="control-51-$(date -u +%Y%m%dT%H%M%SZ)"
RUN_ID="$RUN_ID" NO_CLEANUP=1 \
  USERS=3 JOBS_PER_USER=17 JOBS_PER_MIN=30 \
  NAMESPACES=spark-bench-a,spark-bench-b,spark-bench-c \
  CONTROLLER_THREADS=10 \
  CONTROLLER_MEMORY_REQUEST=4Gi CONTROLLER_MEMORY_LIMIT=16Gi \
  CONTROLLER_CPU_REQUEST=1 CONTROLLER_CPU_LIMIT=4 \
  CONTROLLER_WORKQUEUE_QPS=50 CONTROLLER_WORKQUEUE_BURST=500 \
  CONTROLLER_WORKQUEUE_MAX_DELAY=6h \
  CONTROLLER_KUBE_API_QPS=20 CONTROLLER_KUBE_API_BURST=30 \
  ./run.sh
```

`USERS * JOBS_PER_USER` determines the total application count.
`JOBS_PER_MIN` is the configured submission rate per Locust user. Use a unique
`RUN_ID` for every run; it labels the generated resources and names the results
directory. `NO_CLEANUP=1` retains the applications after collection so that you
can inspect them and capture dashboard evidence before running cleanup.

`run.sh` performs the following checks and preparation automatically:

1. Records component versions, webhook configuration, and cluster capacity.
2. Adds the selected namespaces to the managed `SparkOperator` configuration.
3. Applies and verifies the requested controller resources and tuning.
4. Pre-pulls the workload image and restarts the controller before measurement.
5. Records the controller state before and after the workload.
6. Runs Locust, waits for application lifecycle completion, and exports results.
7. Captures a Prometheus snapshot from Thanos.

The run fails if the capacity preflight fails, requested controller settings do
not reconcile, the controller configuration or pod identity changes during the
workload, the metrics snapshot fails, or Locust exits unsuccessfully.

## Results and Grafana

Run artifacts are written to `results/$RUN_ID/` and are ignored by Git. The
primary files are:

| File | Contents |
| --- | --- |
| `summary.json` | Submission throughput and application start-latency summary. |
| `applications.json` / `applications.csv` | Per-application lifecycle records. |
| `prometheus.json` | Operator, application, and Kubernetes metric queries. |
| `controller-state-before.json` / `controller-state-after.json` | Controller image, resources, selected arguments, pod identity, and restart count. |
| `controller-state.diff` | Must be empty for a stable run. |
| `capacity.json` / `capacity-after.json` | Pre-run qualification and post-run cluster snapshots. |
| `run.log` | Complete benchmark log. |

For visualization, import
`grafana/spark-operator-scale-test-dashboard-rhoai.json` into Grafana, select the
Prometheus-compatible data source connected to the OpenShift Thanos querier,
set the dashboard time range to the run window, and use UTC.

The harness records start latency from accepted `SparkApplication` creation to
the observed driver `Running` and `Ready` state. Use `summary.json` as the
authoritative source for p50, p90, and p99 values; Grafana histogram accuracy is
bounded by the configured histogram buckets.

## Clean up

Delete resources from one run while retaining the benchmark namespaces:

```bash
RUN_ID="$RUN_ID" ./setup/cleanup.sh

oc get sparkapplication --all-namespaces \
  -l "spark-bench.run-id=$RUN_ID"
```

Do not start another measured run until the final command returns no resources.
Leave a five-minute gap between runs when comparing five-minute Prometheus
windows.

To delete all benchmark-labeled applications, omit `RUN_ID`. To also delete the
three benchmark namespaces, set `DELETE_NAMESPACES=true`:

```bash
DELETE_NAMESPACES=true ./setup/cleanup.sh
```

## Workload warning

The checked-in SparkPi workload is intentionally sized for dense benchmark
testing. In particular, `spark.testing.reservedMemory=1` and the `anyuid` SCC
binding are test-specific choices, not production recommendations.

[upstream-benchmark]: https://spark.kubeflow.org/en/latest/performance/benchmarking.html

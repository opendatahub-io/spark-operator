# Kubeflow Spark Operator

Kubernetes operator for managing Apache Spark applications on Kubernetes. Provides CRDs (SparkApplication, ScheduledSparkApplication, SparkConnect), a controller, and a mutating webhook.

## Tech Stack

Go 1.24 | controller-runtime | Kubernetes 1.16+ | Helm 3 | Ginkgo (tests) | Kustomize

## Project Structure

```
api/v1beta2/          # CRD type definitions (SparkApplication, ScheduledSparkApplication)
cmd/operator/         # Entrypoint (main.go) - controller, webhook, version subcommands
internal/controller/  # Core reconciliation logic (sparkapplication/, scheduledsparkapplication/, sparkconnect/)
internal/scheduler/   # Scheduler plugins (kubescheduler, volcano, yunikorn)
internal/webhook/     # Pod mutation webhooks
pkg/                  # Public packages (certificate, client, common, features, scheme, util)
config/               # Kustomize manifests for non-Helm (kustomize) install workflow (see below)
charts/spark-operator-chart/  # Helm chart (templates, values, CRDs, unit tests)
test/e2e/             # E2E tests (Helm install workflow) - Ginkgo suite
examples/openshift/   # OpenShift deployment + integration tests (Kustomize install workflow)
hack/                 # Code generation scripts
```

## Kustomize Install Configuration (config/)

The `config/` directory is used for **Kustomize-based installation** (as opposed to Helm). It defines all Kubernetes resources needed to deploy the operator.

```
config/
├── default/          # Main entry point - composes all sub-components
│   ├── kustomization.yaml   # Orchestrates CRD + RBAC + manager + webhook
│   └── params.env           # Image env vars for controller and webhook
├── crd/              # Auto-generated CRD manifests (from `make manifests`)
├── rbac/             # ClusterRoles, ClusterRoleBindings, ServiceAccounts
├── manager/          # Controller deployment (manager.yaml, serviceaccount.yaml)
├── webhook/          # Webhook deployment, service, mutating/validating configs
├── certmanager/      # TLS certificate resources for webhook
├── overlays/
│   ├── odh/          # OpenDataHub overlay (namespace: opendatahub)
│   └── rhoai/        # Red Hat OpenShift AI overlay (namespace: redhat-ods-applications)
└── samples/          # Example SparkApplication CRs
```

### Container Image Configuration

Images are injected into deployments via **params.env + Kustomize replacements**:

| Env Var in params.env | Used By |
|---|---|
| `RELATED_IMAGE_ODH_SPARK_OPERATOR_IMAGE` | Primary image reference (source of truth) |
| `SPARK_OPERATOR_CONTROLLER_IMAGE` | Injected into controller deployment container image |
| `SPARK_OPERATOR_WEBHOOK_IMAGE` | Injected into webhook deployment container image |

**How it works:** `params.env` defines the image values. Kustomize `configMapGenerator` creates a ConfigMap from it, and `replacements` inject the values into the `$(SPARK_OPERATOR_CONTROLLER_IMAGE)` and `$(SPARK_OPERATOR_WEBHOOK_IMAGE)` placeholders in `config/manager/manager.yaml` and `config/webhook/deployment.yaml`.

**Per-environment image overrides:**
- `config/default/params.env` - Default images
- `config/overlays/odh/params.env` - ODH-specific images (overrides via `behavior: replace`)
- `config/overlays/rhoai/params.env` - RHOAI-specific images (overrides via `behavior: replace`)

Each overlay inherits from `config/default/`, changes the namespace, adds network policies, and can pin different image versions.

## Build

```bash
make build-operator       # Build binary
make generate             # Generate DeepCopy, code-gen
make manifests            # Generate CRD, RBAC, webhook manifests
make docker-build         # Build Docker image
make update-crd           # Sync CRDs to Helm chart
```

## Test

There are tests in TWO locations with different installation workflows:

### Unit Tests
```bash
make unit-test            # Go unit tests (excludes e2e), generates cover-unit.out
```

### E2E Tests - Helm workflow (test/e2e/)
```bash
make kind-create-cluster  # Create KIND cluster
make kind-load-image      # Load operator image
make e2e-test             # Run Ginkgo e2e tests
```
Uses Ginkgo BDD framework. Suite installs operator via Helm chart. Generates `cover-e2e.out`. See `test/e2e/suite_test.go`.

### Integration Tests - Kustomize workflow (examples/openshift/tests/)
```bash
cd examples/openshift
make kind-setup           # Create KIND cluster + namespaces
make operator-install     # Install via Kustomize
make test-all             # Run all integration tests
make kind-cleanup         # Teardown
```
Shell-script based tests: `test-operator-install.sh`, `test-spark-pi.sh`, `test-docling-spark.sh`.
Env vars: `CLEANUP=true`, `KIND_CLUSTER_NAME=spark-operator`, `TIMEOUT_SECONDS=600`.

> **Note:** The team plans to consolidate tests under `test/` once upstream parity is achieved.

### Helm Chart Tests
```bash
make helm-unittest        # Helm chart unit tests
make helm-lint            # Lint chart
```

## Lint & Format

```bash
make go-fmt               # Format Go code
make go-vet               # Run go vet
make go-lint              # Run golangci-lint
make go-lint-fix          # Auto-fix lint issues
make detect-crds-drift    # Check CRD drift between config/ and charts/
```

Config: `.golangci.yaml`

## Key Files

### SparkApplication CRD
- `api/v1beta2/sparkapplication_types.go` - SparkApplication CRD type definitions
- `internal/controller/sparkapplication/controller.go` - Main reconciliation loop
- `internal/controller/sparkapplication/submission.go` - Spark submission logic

### ScheduledSparkApplication CRD
- `api/v1beta2/scheduledsparkapplication_types.go` - ScheduledSparkApplication CRD type definitions
- `internal/controller/scheduledsparkapplication/controller.go` - Cron-based scheduling controller

### SparkConnect CRD (Alpha)
- `api/v1alpha1/sparkconnect_types.go` - SparkConnect CRD type definitions (v1alpha1)
- `internal/controller/sparkconnect/reconciler.go` - SparkConnect reconciliation logic
- `internal/controller/sparkconnect/options.go` - SparkConnect configuration options

### Other Key Files
- `cmd/operator/main.go` - Operator entrypoint
- `charts/spark-operator-chart/values.yaml` - Helm default values
- `config/default/kustomization.yaml` - Default Kustomize overlay
- `VERSION` - Current version (v2.3.0)

## CI

GitHub Actions (`.github/workflows/`):
- `integration.yaml` - code-check, unit-test, helm-test, e2e on KIND
- `openshift-spark-pi-e2e.yaml` / `openshift-docling-e2e.yaml` - OpenShift integration tests
- `release.yaml` - Release builds and publishes
- Coverage uploaded to Codecov with separate flags: `unit` and `e2e` (see `.codecov.yml`)

## Debugging

- Structured logging via controller-runtime's `logr`
- Prometheus metrics exposed at `/metrics` (see `internal/metrics/`)
- Check operator logs: `kubectl logs -n spark-operator deployment/spark-operator-controller`
- Check SparkApplication status: `kubectl describe sparkapplication <name>`
- For test debugging, set `CLEANUP=false` in openshift tests to preserve resources

# Spark Operator Module

Thin deployment orchestrator for the [Spark workload operator](https://github.com/kubeflow/spark-operator) on [Open Data Hub](https://opendatahub.io/) / RHOAI platforms.

## What is Spark Operator Module?

The Spark Operator Module is a Kubernetes controller that bridges the ODH/RHOAI platform and the upstream Kubeflow Spark Operator. It watches a platform-level custom resource (`SparkOperator`) and handles the full lifecycle of the Spark workload operator — deploying, updating, health-checking, and cleaning up.

```
Platform (ODH/RHOAI)
  └─ SparkOperator CR (components.platform.opendatahub.io/v1alpha1)
       └─ Module Operator (this) ← deploys & manages
            └─ Spark Workload Operator (kubeflow/spark-operator)
                 └─ SparkApplication CRs (sparkoperator.k8s.io/v1beta2)
```



### Key Features

- Watches a cluster-scoped singleton `SparkOperator` CR (`default-sparkoperator`)
- Renders Kustomize manifests from the parent repo's `config/` directory
- Deploys resources using Server-Side Apply (SSA) with `ForceOwnership` for zero-downtime adoption
- Detects platform type (ODH vs RHOAI) and selects the correct overlay and namespace
- Checks deployment health (`ObservedGeneration`, `UpdatedReplicas`, `AvailableReplicas`)
- Reports status conditions (`Ready`, `ProvisioningSucceeded`, `Degraded`) back to the platform
- Supports `Managed` / `Removed` management states with full cleanup on removal

## Project Structure

```
spark-operator-module/
├── cmd/spark-operator-module/main.go           # Entrypoint
├── pkg/
│   ├── apis/v1alpha1/                          # Platform CRD types
│   │   ├── types.go                            # SparkOperator CR definition
│   │   ├── groupversion_info.go                # GVK registration
│   │   └── zz_generated.deepcopy.go            # Generated DeepCopy methods
│   └── sparkoperatormodule/                    # Controller logic
│       ├── reconciler.go                       # Main reconciliation loop
│       ├── resource_manager.go                 # Deploy, cleanup, readiness checks
│       ├── status.go                           # Status condition management
│       ├── params.go                           # params.env parsing & image substitution
│       ├── images.go                           # Container image resolution
│       ├── releases.go                         # Component release tracking
│       ├── components.go                       # Component configuration
│       ├── constants.go                        # Shared constants
│       ├── setup.go                            # Controller setup & manager wiring
│       └── fixture/                            # Test helpers
│           ├── envtest.go                      # envtest bootstrap
│           ├── helpers.go                      # Deployment & CR helpers
│           ├── mock_deployer.go                # Mock deployer for unit tests
│           └── sparkoperator_builder.go        # CR builder for tests
├── config/
│   ├── crd/                                    # Generated CRD manifest
│   ├── rbac/                                   # ClusterRole, bindings, service account
│   ├── manager/                                # Controller Deployment
│   └── default/                                # Kustomize composition entry point
├── hack/
│   └── get_spark_manifests.sh                  # Fetch workload operator manifests
└── go.mod                                      # Independent Go module
```



## Prerequisites

- Go >= 1.25
- [Kustomize](https://kustomize.io/) >= 5.0
- [controller-gen](https://book.kubebuilder.io/reference/controller-gen) (installed via `make controller-gen`)
- [envtest](https://book.kubebuilder.io/reference/envtest) binaries (installed via `make envtest`)
- Access to a Kubernetes cluster (for deployment)



## Build & Test

All commands are run from the **repository root** using targets in `Makefile.spark-operator-module.mk`:

```bash
# Full precommit check (generate + test + build + verify clean tree)
make check-som

# Generate code and manifests
make generate-spark-operator-module    # DeepCopy methods
make manifests-spark-operator-module   # CRD + RBAC manifests

# Run unit + integration tests (uses envtest)
make test-spark-operator-module

# Build container image
make docker-build-spark-operator-module

# Push container image
make docker-push-spark-operator-module

# Deploy to cluster via Kustomize + SSA
make deploy-spark-operator-module

# Render manifests without applying
make kustomize-build-spark-operator-module
```



## Custom Resource

The module defines a cluster-scoped singleton CR:

```yaml
apiVersion: components.platform.opendatahub.io/v1alpha1
kind: SparkOperator
metadata:
  name: default-sparkoperator    # enforced by CEL validation
spec:
  managementState: Managed       # Managed | Removed
```



### Status

```bash
kubectl get sparkoperator default-sparkoperator
```


| Condition               | Description                                           |
| ----------------------- | ----------------------------------------------------- |
| `Ready`                 | All workload operator deployments are healthy         |
| `ProvisioningSucceeded` | Manifests rendered and applied successfully           |
| `Degraded`              | Controller encountered an error during reconciliation |


## Configuration


| Environment Variable      | Description                              | Default          |
| ------------------------- | ---------------------------------------- | ---------------- |
| `APPLICATIONS_NAMESPACE`  | Override target namespace for deployment | Auto-detected    |
| `MANIFESTS_TEMPLATE_PATH` | Path to workload operator manifests      | `/opt/manifests` |


Platform detection automatically selects the namespace:

- **ODH**: `opendatahub`
- **RHOAI (managed/self-managed)**: `redhat-ods-applications`

## CI

The module has its own GitHub Actions workflow (`.github/workflows/spark-operator-module-ci.yaml`) that runs on PRs touching module files. It executes `make check-som` which:

1. Generates code and manifests
2. Runs unit + integration tests
3. Builds the Go binary
4. Verifies the git tree is clean (no uncommitted generated changes)
5. Verifies root codegen is not cross-contaminated


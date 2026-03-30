# OpenShift/KIND E2E Tests - Local Development Guide

This directory contains end-to-end tests for the Spark Operator. These tests work on both:
- **KIND clusters** (local development)
- **OpenShift clusters** (production)

The Makefile at `examples/openshift/Makefile` provides standardized make targets that can be used in GitHub Actions CI and locally on Mac/Linux.

## Overview

### What's Tested

| Test | Type | What It Validates |
|------|------|-------------------|
| **Operator Install** | Shell | Kustomize manifests work, fsGroup ≠ 185, non-root UID |
| **Spark Pi** | Shell | SparkApplication CRD works, Driver/Executor pods run, job completes |
| **Docling Spark** | Shell | PDF-to-markdown conversion, PVC storage, multi-executor workload |
| **Go E2E (Kustomize)** | Go/Ginkgo | Full upstream e2e test suite using Kustomize manifests for operator installation |

---

## Prerequisites

- **Docker** - Running and accessible
- **kubectl** - Kubernetes CLI
- **Go** (1.24+) - Required for Go e2e tests
- **kind** - For local KIND cluster setup (install via `go install sigs.k8s.io/kind` or from [kind releases](https://kind.sigs.k8s.io/docs/user/quick-start/#installation))

---

## Quick Start

> **Important:** Run all make commands from the `examples/openshift/` directory.

```bash
cd /path/to/spark-operator/examples/openshift
```

### Step 1: Setup Kind Cluster (for local testing only)

```bash
make kind-setup
```

This creates:
- 2-node Kind cluster (`spark-operator`)
- `spark-operator` namespace
- Input/output PVCs (Kind-compatible)

For full setup with docling image (~9.5GB):
```bash
make kind-setup-full
```

> **Note:** Skip this step if testing on an existing OpenShift cluster.

### Step 2: Install Spark Operator

```bash
make operator-install
```

Or keep operator installed for subsequent tests:
```bash
CLEANUP=false make operator-install
```

### Step 3: Run Tests

**Run Spark Pi test (shell script):**
```bash
make test-spark-pi
```

**Run Docling Spark test:**
```bash
make test-docling-spark
```

**Run all tests:**
```bash
make test-all
```

### Step 4: Cleanup (KIND only)

```bash
make kind-cleanup
```

---

## Make Targets

| Target | Description |
|--------|-------------|
| `make kind-setup` | Setup local Kind cluster for testing |
| `make kind-setup-full` | Setup Kind + pull docling image + upload test PDFs |
| `make kind-cleanup` | Delete Kind cluster and cleanup resources |
| `make operator-install` | Install Spark operator (auto-runs `kind-setup` if no cluster) |
| `make test-spark-pi` | Run Spark Pi test (auto-runs `operator-install` if needed) |
| `make test-docling-spark` | Run Docling Spark test (auto-runs `operator-install` if needed) |
| `make e2e-kustomize-test` | Run Go e2e tests with Kustomize-based operator installation |
| `make test-all` | Run all shell tests (operator-install + spark-pi + docling) |

---

## Configuration Options

All test targets (`operator-install`, `test-spark-pi`, `test-docling-spark`) support the `CLEANUP` environment variable:

```bash
# Default behavior (cleanup after test)
make test-spark-pi

# Keep resources for debugging
CLEANUP=false make test-spark-pi
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CLEANUP` | `true` | Set to `false` to preserve resources after tests |
| `KIND_CLUSTER_NAME` | `spark-operator` | Name of the Kind cluster |
| `K8S_VERSION` | `v1.32.0` | Kubernetes version for Kind |
| `KIND_KUBE_CONFIG` | `~/.kube/config` | Kubeconfig file path |
| `TIMEOUT_SECONDS` | `600` | Max wait time for shell tests |

### Examples

```bash
# Use a different cluster name and Kubernetes version
KIND_CLUSTER_NAME=spark-test K8S_VERSION=v1.30.8 make kind-setup

# Keep resources for debugging
CLEANUP=false make test-spark-pi

# Run full test suite
CLEANUP=false make operator-install
CLEANUP=false make test-spark-pi
make test-docling-spark
```

---

## Go E2E Tests (Kustomize)

The `e2e/` subdirectory contains a copy of the upstream Go e2e test suite (`test/e2e/`) adapted to install the operator using Kustomize manifests (`config/default/`) instead of Helm. This allows testing the Kustomize-based deployment path with the same SparkApplication and SparkConnect test cases the upstream project uses.

### How it works

The test suite in `e2e/suite_test.go` supports a toggle via the `INSTALL_METHOD` environment variable:

- `INSTALL_METHOD=helm` (default) — installs the operator using the Helm chart (same as `test/e2e/`)
- `INSTALL_METHOD=kustomize` — installs the operator using `kubectl apply -k config/default/ --server-side=true`

When using Kustomize mode, the test also:
- Overrides the operator image in `config/default/params.env` if `SPARK_OPERATOR_IMAGE` is set
- Creates the Spark driver ServiceAccount and RBAC in the `default` namespace (matching what the Helm chart provides for test workloads)

### Running locally

**Prerequisites:** A Kind cluster with the operator image built from source and loaded. From the repo root:

```bash
# Build operator from source, create Kind cluster, and load image
make kind-load-image IMAGE_TAG=local

# Run the tests (SPARK_OPERATOR_IMAGE overrides the default in params.env)
SPARK_OPERATOR_IMAGE=ghcr.io/kubeflow/spark-operator/controller:local \
  make -C examples/openshift e2e-kustomize-test
```

Or step by step for debugging:

```bash
# 1. Build and load image into Kind
make kind-load-image IMAGE_TAG=local

# 2. Run from repo root with environment variables
INSTALL_METHOD=kustomize \
  SPARK_OPERATOR_IMAGE=ghcr.io/kubeflow/spark-operator/controller:local \
  go test ./examples/openshift/tests/e2e/ -v -ginkgo.v -timeout 30m
```

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `INSTALL_METHOD` | `helm` | Set to `kustomize` to use Kustomize manifests for operator installation |
| `SPARK_OPERATOR_IMAGE` | *(uses `params.env` default)* | Overrides the controller and webhook image in `config/default/params.env` before applying. Set to the locally built image for development/CI. |

### CI integration

The `kustomize-e2e-test` job in `.github/workflows/kustomize-e2e.yaml` builds the operator image from the PR, loads it into Kind, and runs these tests with `SPARK_OPERATOR_IMAGE` set to the locally built image. It triggers on PRs that touch `config/`, `examples/openshift/tests/e2e/`, operator source code, or the workflow file itself, and runs across the same Kubernetes version matrix as the upstream Helm-based e2e tests.

### File structure

| File | Purpose |
|------|---------|
| `e2e/suite_test.go` | Test suite setup with Helm/Kustomize toggle, webhook readiness checks |
| `e2e/sparkapplication_test.go` | SparkApplication e2e specs (spark-pi, configmap, custom-resource, failures, suspend/resume) |
| `e2e/sparkconnect_test.go` | SparkConnect reconciliation spec |
| `e2e/bad_examples/` | YAML fixtures for failure/retry test cases |
---

## Shell Test Details

### test-operator-install.sh

Validates:
1. Spark Operator installs from Kustomize manifests
2. **fsGroup is NOT 185** (critical for OpenShift security)
3. Container runs with non-root UID
4. Controller and Webhook pods are Ready

### test-spark-pi.sh

Validates:
1. SparkApplication CRD can be submitted
2. Driver pod starts and runs
3. Executor pods are created
4. Application completes successfully
5. Pi calculation result appears in logs

### test-docling-spark.sh

Validates:
1. Docling Spark workload submits and completes
2. Driver pod starts and runs
3. Executor pods are created
4. Application completes successfully

---

## GitHub Actions Integration

These make targets are designed to work in GitHub Actions CI.

### Shell tests example

```yaml
- name: Setup Kind cluster
  run: make -C examples/openshift kind-setup

- name: Install operator
  run: CLEANUP=false make -C examples/openshift operator-install

- name: Run Spark Pi test
  run: CLEANUP=false make -C examples/openshift test-spark-pi

- name: Run Docling Spark test
  run: CLEANUP=false make -C examples/openshift test-docling-spark

- name: Cleanup
  if: always()
  run: make -C examples/openshift kind-cleanup
```

### Go e2e tests (Kustomize)

See `.github/workflows/kustomize-e2e.yaml` for the full workflow. Key steps:

```yaml
- name: Create a Kind cluster
  run: make kind-create-cluster KIND_K8S_VERSION=v1.32.0

- name: Build and load image to Kind cluster
  run: make kind-load-image IMAGE_TAG=local

- name: Run kustomize e2e tests
  run: make -C examples/openshift e2e-kustomize-test
  env:
    SPARK_OPERATOR_IMAGE: ghcr.io/kubeflow/spark-operator/controller:local
```

> **Note:** `make -C examples/openshift` runs make from the repo root but changes to the `examples/openshift/` directory first. Alternatively, `cd examples/openshift && make` works the same way.
---

## Architecture

```
┌───────────────────────────────────────────────────────────┐
│                      Kind Cluster                         │
│  ┌─────────────────────────────────────────────────────┐  │
│  │              spark-operator namespace               │  │
│  │  ┌─────────────────┐  ┌─────────────────────────┐   │  │
│  │  │   Controller    │  │       Webhook           │   │  │
│  │  │      Pod        │  │         Pod             │   │  │
│  │  └─────────────────┘  └─────────────────────────┘   │  │
│  │                                                     │  │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  │  │
│  │  │   Driver    │  │  Executor   │  │    PVCs     │  │  │
│  │  │    Pod      │  │    Pods     │  │ input/output│  │  │
│  │  └─────────────┘  └─────────────┘  └─────────────┘  │  │
│  └─────────────────────────────────────────────────────┘  │
└───────────────────────────────────────────────────────────┘
```

---

## Files in This Directory

| File | Purpose |
|------|---------|
| `setup-kind-cluster.sh` | Creates Kind cluster and prerequisites |
| `cleanup-kind-cluster.sh` | Deletes Kind cluster and resources |
| `test-operator-install.sh` | Tests operator installation from Kustomize manifests |
| `test-spark-pi.sh` | Tests Spark Pi application |
| `test-docling-spark.sh` | Tests Docling Spark workload |
| `spark-pi-app.yaml` | SparkApplication manifest for Spark Pi |
| `assets/` | Test PDF files for docling tests |
| `e2e/` | Go e2e test suite (Ginkgo) with Helm/Kustomize toggle |
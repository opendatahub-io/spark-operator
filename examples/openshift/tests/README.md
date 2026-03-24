# OpenShift/KIND E2E Tests - Local Development Guide

This directory contains end-to-end tests for the Spark Operator. These tests work on both:
- **KIND clusters** (local development)
- **OpenShift clusters** (production)

The Makefile at `examples/openshift/Makefile` provides standardized make targets that can be used in GitHub Actions CI and locally on Mac/Linux.

## Overview

### What's Tested

| Test | What It Validates |
|------|-------------------|
| **Operator Install** | Kustomize manifests work, fsGroup ≠ 185, non-root UID |
| **CRDs** | All 3 CRDs exist, are Established, serve expected API versions |
| **Webhooks** | Mutating/Validating webhook configs have CA bundles, endpoints healthy, invalid resources rejected |
| **Spark Pi** | SparkApplication CRD works, Driver/Executor pods run, job completes |
| **Submission Failure** | Submission failure retries work, no driver pod created, correct attempt count |
| **Application Failure** | Application failure retries work, driver pod created, correct attempt count |
| **Docling Spark** | PDF-to-markdown conversion, PVC storage, multi-executor workload |

---

## Prerequisites

- **Docker** - Running and accessible
- **kubectl** - Kubernetes CLI
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

**Run the full Kustomize E2E suite:**
```bash
make test-kustomize-e2e
```

**Or run individual tests:**
```bash
make test-crds                   # Verify CRDs
make test-webhooks               # Verify webhooks
make test-spark-pi               # Spark Pi happy path
make test-submission-failure     # Submission failure retries
make test-application-failure    # Application failure retries
make test-docling-spark          # Docling Spark workload (requires kind-setup-full)
```

**Run all tests (including docling):**
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
| `make test-crds` | Verify CRDs are installed, Established, and discoverable |
| `make test-webhooks` | Verify webhooks have CA bundles, healthy endpoints, reject invalid resources |
| `make test-spark-pi` | Run Spark Pi test (auto-runs `operator-install` if needed) |
| `make test-submission-failure` | Test submission failure handling with retries |
| `make test-application-failure` | Test application failure handling with retries |
| `make test-docling-spark` | Run Docling Spark test (auto-runs `operator-install` if needed) |
| `make test-kustomize-e2e` | Run full Kustomize E2E suite (install + CRDs + webhooks + spark-pi + failure tests) |
| `make test-all` | Run all tests (operator-install + spark-pi + docling) |

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
| `SPARK_IMAGE` | `quay.io/rishasin/docling-spark@sha256:7e8...` | Spark image (pinned digest) used in test fixtures |

### Examples

```bash
# Use a different cluster name and Kubernetes version
KIND_CLUSTER_NAME=spark-test K8S_VERSION=v1.30.8 make kind-setup

# Keep resources for debugging
CLEANUP=false make test-spark-pi

# Run full Kustomize E2E suite
make test-kustomize-e2e

# Run full test suite step by step
CLEANUP=false make operator-install
make test-crds
make test-webhooks
CLEANUP=false make test-spark-pi
CLEANUP=false make test-submission-failure
make test-application-failure
```

---

## Test Details

### test-operator-install.sh

Validates:
1. Spark Operator installs from Kustomize manifests
2. **fsGroup is NOT 185** (critical for OpenShift security)
3. Container runs with non-root UID
4. Controller and Webhook pods are Ready

### test-crds.sh

Validates:
1. All 3 CRDs exist (`sparkapplications`, `scheduledsparkapplications`, `sparkconnects`)
2. Each CRD has `Established=True` condition
3. CRDs serve expected API versions (`v1beta2` for SparkApplication/ScheduledSparkApplication, `v1alpha1` for SparkConnect)
4. All API resources are discoverable via `kubectl api-resources`

### test-webhook-validation.sh

Validates:
1. `MutatingWebhookConfiguration` exists with CA bundles injected
2. `ValidatingWebhookConfiguration` exists with CA bundles injected
3. Webhook Service (`spark-operator-webhook-svc`) has healthy endpoints
4. Validating webhook rejects an invalid SparkApplication (DNS-1035 name violation)

### test-spark-pi.sh

Validates:
1. SparkApplication CRD can be submitted
2. Driver pod starts and runs
3. Executor pods are created
4. Application completes successfully
5. Pi calculation result appears in logs

### test-submission-failure.sh

Validates:
1. SparkApplication with non-existent ServiceAccount triggers submission failure
2. Operator retries submission (`onSubmissionFailureRetries: 2`)
3. Final state is `FAILED` with correct `submissionAttempts` count (3)
4. No driver pod is created (failure happens before pod creation)

### test-application-failure.sh

Validates:
1. SparkApplication with non-existent mainClass/jar triggers application failure
2. Operator retries execution (`onFailureRetries: 2`)
3. Final state is `FAILED` with correct `executionAttempts` count (3)
4. Driver pod is created (submission succeeds, application fails at runtime)
5. Error messages appear in driver logs

### test-docling-spark.sh

Validates:
1. Docling Spark workload submits and completes
2. Driver pod starts and runs
3. Executor pods are created
4. Application completes successfully

---

## GitHub Actions Integration

These make targets are designed to work in GitHub Actions CI. There are two workflows:

### Kustomize E2E Workflow (`.github/workflows/kustomize-e2e.yaml`)

Runs on **every PR** (no path filtering) against a Kubernetes version matrix (`v1.28.15`, `v1.30.8`, `v1.32.0`). Tests CRDs, webhooks, Spark Pi, and failure handling:

```yaml
- name: Setup Kind cluster
  run: make -C examples/openshift kind-setup

- name: Install operator
  run: CLEANUP=false make -C examples/openshift operator-install

- name: Run CRD tests
  run: make -C examples/openshift test-crds

- name: Run webhook tests
  run: make -C examples/openshift test-webhooks

- name: Run Spark Pi test
  run: CLEANUP=false make -C examples/openshift test-spark-pi

- name: Run submission failure test
  run: CLEANUP=false make -C examples/openshift test-submission-failure

- name: Run application failure test
  run: make -C examples/openshift test-application-failure

- name: Cleanup
  if: always()
  run: make -C examples/openshift kind-cleanup
```

### Existing Integration Workflow (`.github/workflows/integration.yaml`)

Runs Helm-based Go E2E tests.

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
| `test-crds.sh` | Verifies CRDs are installed, Established, and discoverable |
| `test-webhook-validation.sh` | Verifies webhooks have CA bundles, healthy endpoints, reject invalid resources |
| `test-spark-pi.sh` | Tests Spark Pi application (happy path) |
| `test-submission-failure.sh` | Tests submission failure retries with non-existent ServiceAccount |
| `test-application-failure.sh` | Tests application failure retries with invalid mainClass/jar |
| `spark-pi-app.yaml` | SparkApplication manifest for Spark Pi |
| `fixtures/` | Test fixture YAMLs (invalid SparkApp, failure-triggering apps) |
| `assets/` | Test PDF files for docling tests |
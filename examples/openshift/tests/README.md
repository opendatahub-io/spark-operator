# OpenShift/KIND E2E Tests - Local Development Guide

This directory contains end-to-end tests for the Spark Operator. These tests work on both:
- **KIND clusters** (local development)
- **OpenShift clusters** (production)

The same make targets can be used in GitHub Actions CI and locally on Mac/Linux.

## Overview

### What's Tested

| Test | What It Validates |
|------|-------------------|
| **Operator Install** | Helm chart works, fsGroup ≠ 185, jobNamespaces configured |
| **Spark Pi** | SparkApplication CRD works, Driver/Executor pods run, job completes |
| **Docling Spark** | PDF-to-markdown conversion, PVC storage, multi-executor workload (via Go tests) |

---

## Prerequisites

- **Docker** - Running and accessible
- **kubectl** - Kubernetes CLI
- **helm** - Helm CLI (installed automatically via `make helm`)
- **go** - For installing Kind via Makefile and running Go tests

---

## Quick Start

> **Important:** Run all commands from the **repository root** directory (`spark-operator/`), not from this tests directory.

```bash
cd /path/to/spark-operator
```

### Step 1: Setup Kind Cluster (for local testing only)

```bash
make kind-setup
```

This creates:
- 2-node Kind cluster (`spark-operator`)
- `spark-operator` namespace
- Input/output PVCs (Kind-compatible)
- RBAC (ServiceAccount, Role, RoleBinding)

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

**Run Docling Spark test (Go e2e tests):**
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
| `make operator-install` | Install Spark operator on KIND or OpenShift |
| `make test-spark-pi` | Run Spark Pi test (shell script) |
| `make test-docling-spark` | Run Docling Spark test (Go e2e tests) |
| `make test-all` | Run all tests (operator-install + spark-pi + docling Go tests) |

---

## Configuration Options

Shell script targets (`operator-install`, `test-spark-pi`) support the `CLEANUP` environment variable:

```bash
# Default behavior (cleanup after test)
make test-spark-pi

# Keep resources for debugging
CLEANUP=false make test-spark-pi
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CLEANUP` | `true` | Set to `false` to preserve resources after shell tests |
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

## Test Details

### test-operator-install.sh

Validates:
1. Spark Operator installs from OpenDataHub Helm repo
2. **fsGroup is NOT 185** (critical for OpenShift security)
3. `jobNamespaces` configured correctly
4. Controller and Webhook pods are Ready

### test-spark-pi.sh

Validates:
1. SparkApplication CRD can be submitted
2. Driver pod starts and runs
3. Executor pods are created
4. Application completes successfully
5. Pi calculation result appears in logs

### Go Tests (openshift_test.go)

The Go tests (`make test-docling-spark`) provide comprehensive validation:
1. Verifies fsGroup is NOT 185
2. Runs the docling-spark-app workload
3. Verifies driver pod completion and security context

```bash
make test-docling-spark
```

---

## GitHub Actions Integration

These make targets are designed to work in GitHub Actions CI. Example workflow usage:

```yaml
- name: Setup Kind cluster
  run: make kind-setup

- name: Install operator
  run: CLEANUP=false make operator-install

- name: Run Spark Pi test
  run: CLEANUP=false make test-spark-pi

- name: Run Docling test (Go e2e)
  run: make test-docling-spark

- name: Cleanup
  if: always()
  run: make kind-cleanup
```

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
| `test-operator-install.sh` | Tests operator installation from Helm |
| `test-spark-pi.sh` | Tests Spark Pi application |
| `suite_test.go` | Go test suite setup |
| `openshift_test.go` | Go E2E tests (docling workload) |
| `spark-pi-app.yaml` | SparkApplication manifest for Spark Pi |
| `assets/` | Test PDF files for docling tests |

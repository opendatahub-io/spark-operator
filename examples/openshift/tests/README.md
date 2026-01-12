# OpenShift E2E Tests - Local Development Guide

This directory contains end-to-end tests for the Spark Operator on OpenShift-compatible environments. These tests can be run locally using a Kind (Kubernetes in Docker) cluster for fast iteration before deploying to real OpenShift.

## Overview

### What's Tested Locally

| Test | What It Validates |
|------|-------------------|
| **Operator Install** | Helm chart works, fsGroup ≠ 185, jobNamespaces configured |
| **Spark Pi** | SparkApplication CRD works, Driver/Executor pods run, job completes |
| **Go E2E Tests** | Programmatic verification of all above + docling workload |

---

## Prerequisites

- **Docker** - Running and accessible
- **kubectl** - Kubernetes CLI
- **helm** - Helm CLI (installed automatically via `make helm`)
- **go** - For installing Kind via Makefile

---

## Quick Start

> **Important:** Run all commands from the **repository root** directory (`spark-operator/`), not from this tests directory.

```bash
cd /path/to/spark-operator
```

### Step 1: Setup Kind Cluster

Choose ONE of the following options:

#### Option A: Basic Setup (Recommended)
```bash
make openshift-test-setup
```
This creates:
- 2-node Kind cluster (`spark-operator`)
- `docling-spark` namespace
- Input/output PVCs (Kind-compatible)
- RBAC (ServiceAccount, Role, RoleBinding)

#### Option B: Full Setup with Docling Image
```bash
make openshift-test-setup-full
```
Same as basic setup, PLUS:
- Pulls the 9.5GB `docling-spark` image
- Uploads test PDF files to input PVC

> ⚠️ **Apple Silicon (M1/M2) Limitation:** `openshift-test-setup-full` does NOT work on ARM64 Macs because the `docling-spark` image is only built for AMD64/x86. Use `openshift-test-setup` instead.

### Step 2: Run Tests

```bash
make openshift-test-shell
```

This installs the Spark Operator and runs the Spark Pi test.

### Step 3: Cleanup

```bash
make openshift-test-cleanup
```

---

## Configuration Options

You can override default values using **environment variables**. No file changes needed - just prefix the command with the variable.

### How to Override

**Syntax:** `VARIABLE=value make target` or `VARIABLE=value ./script.sh`

### Cluster Configuration

| Variable | Default | Description | Example |
|----------|---------|-------------|---------|
| `KIND_CLUSTER_NAME` | `spark-operator` | Name of the Kind cluster | `KIND_CLUSTER_NAME=my-test make openshift-test-setup` |
| `K8S_VERSION` | `v1.32.0` | Kubernetes version | `K8S_VERSION=v1.30.8 make openshift-test-setup` |
| `KIND_KUBE_CONFIG` | `~/.kube/config` | Kubeconfig file path | `KIND_KUBE_CONFIG=/tmp/kubeconfig make openshift-test-setup` |

### Test Configuration

| Variable | Default | Description | Example |
|----------|---------|-------------|---------|
| `TIMEOUT` | `5m` | Helm install timeout | `TIMEOUT=10m make openshift-test-shell` |
| `CLEANUP` | `true` | Cleanup after operator install test | `CLEANUP=false make openshift-test-shell` |
| `SKIP_CLEANUP` | `false` | Keep Spark app after test | `SKIP_CLEANUP=true ./examples/openshift/tests/test-spark-pi.sh` |

### Examples

```bash
# Use a different cluster name and Kubernetes version
KIND_CLUSTER_NAME=spark-test K8S_VERSION=v1.30.8 make openshift-test-setup

# Run with longer timeout (useful for first run when images are being pulled)
TIMEOUT=10m make openshift-test-shell

# Keep the operator installed after tests for debugging
CLEANUP=false make openshift-test-shell
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

```bash
make openshift-test
```
---

## Architecture

```
┌───────────────────────────────────────────────────────────┐
│                      Kind Cluster                         │
│  ┌─────────────────────────────────────────────────────┐  │
│  │           spark-operator-openshift namespace        │  │
│  │  ┌─────────────────┐  ┌─────────────────────────┐   │  │
│  │  │   Controller    │  │       Webhook           │   │  │
│  │  │      Pod        │  │         Pod             │   │  │
│  │  └─────────────────┘  └─────────────────────────┘   │  │
│  └─────────────────────────────────────────────────────┘  │
│                                                           │
│  ┌─────────────────────────────────────────────────────┐  │
│  │              docling-spark namespace                │  │
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
| `openshift_test.go` | Go E2E tests |
| `spark-pi-app.yaml` | SparkApplication manifest for tests |
| `assets/` | Test PDF files for docling tests |

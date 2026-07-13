# E2E Tests: Preinstalled Mode

## Overview

The upstream e2e test suite now supports running tests against clusters where the Spark operator is already deployed using the `PREINSTALLED=true` flag.

This is useful for:

- **Managed Kubernetes clusters** (EKS, GKE, AKS) with pre-deployed operators
- **OpenShift/RHOAI clusters** where operator is managed by DataScienceCluster or Kustomize overlays
- **Development workflows** - install operator once, run tests multiple times
- **CI scenarios** where operator installation is separate from test execution

## Usage

### Basic Usage

```bash
# Install operator first (via any method - helm or kustomize)
kubectl apply -k config/overlays/rhoai

# Run tests with preinstalled mode
# Specify DEPLOY_METHOD to match how operator was installed
DEPLOY_METHOD=kustomize PREINSTALLED=true make e2e-test
```

### Environment Variables

| Variable | Values | Purpose |
|----------|--------|---------|
| `DEPLOY_METHOD` | `helm` (default), `kustomize` | Determines webhook names to look for |
| `PREINSTALLED` | `true`, `false` (default) | Skip operator install/uninstall when true |

**Important**: `DEPLOY_METHOD` must match how the operator was actually installed to find the correct webhook names.

### Examples

#### Default (Tests Install via Helm)
```bash
# Tests manage operator lifecycle - install via Helm, uninstall after
make e2e-test
# Equivalent to:
DEPLOY_METHOD=helm PREINSTALLED=false make e2e-test
```

#### Tests Install via Kustomize
```bash
# Tests install via Kustomize, uninstall after
DEPLOY_METHOD=kustomize IMAGE_TAG=local make e2e-test
```

#### Preinstalled (Operator Installed via Helm)
```bash
# Operator already installed via Helm - use helm webhooks, skip install
DEPLOY_METHOD=helm PREINSTALLED=true make e2e-test
```

#### Preinstalled (Operator Installed via Kustomize)
```bash
# Operator already installed via Kustomize - use kustomize webhooks, skip install
DEPLOY_METHOD=kustomize PREINSTALLED=true make e2e-test
```

## How It Works

### BeforeSuite Behavior

**helm/kustomize modes** (manage lifecycle):
```
BeforeSuite:
  1. Create namespace
  2. Install operator (helm or kustomize)
  3. Wait for webhooks ready
  
AfterSuite:
  1. Uninstall operator
  2. Delete namespace
```

**preinstalled mode** (use existing):
```
BeforeSuite:
  1. Skip installation
  2. Wait for webhooks ready (assumes operator running)
  
AfterSuite:
  1. Skip uninstallation (leaves operator running)
```

### Webhook Configuration

The test suite automatically detects webhook names based on install method:

| Install Method | Mutating Webhook | Validating Webhook |
|----------------|------------------|-------------------|
| `helm` | `spark-operator-webhook` | `spark-operator-webhook` |
| `kustomize` | `mutating-webhook-configuration` | `validating-webhook-configuration` |
| `preinstalled` | `mutating-webhook-configuration` | `validating-webhook-configuration` |

## RHOAI/OpenShift Example

### Setup
```bash
# Install operator via RHOAI overlay (uses Kustomize)
kubectl apply -k config/overlays/rhoai
kubectl wait --for=condition=Available -n redhat-ods-applications \
  deployment/spark-operator-controller --timeout=5m
kubectl wait --for=condition=Available -n redhat-ods-applications \
  deployment/spark-operator-webhook --timeout=5m
```

### Run Tests
```bash
# All upstream tests run against RHOAI-deployed operator
# Use DEPLOY_METHOD=kustomize because RHOAI overlay uses Kustomize
DEPLOY_METHOD=kustomize PREINSTALLED=true go test ./test/e2e/... -v -ginkgo.v

# Or via Makefile
DEPLOY_METHOD=kustomize PREINSTALLED=true make e2e-test
```

### Cleanup
```bash
# Operator stays running after tests (not deleted)
# Manually cleanup if needed:
kubectl delete -k config/overlays/rhoai
```

## Development Workflow

### Install Once, Test Many Times

```bash
# One-time setup
make kind-create-cluster
make kind-load-image IMAGE_TAG=dev
DEPLOY_METHOD=kustomize IMAGE_TAG=dev make e2e-test  # Installs operator

# Now iterate on test code (operator stays running)
DEPLOY_METHOD=kustomize PREINSTALLED=true make e2e-test  # Fast!
# ... edit test code ...
DEPLOY_METHOD=kustomize PREINSTALLED=true make e2e-test  # Fast!
# ... edit test code ...
DEPLOY_METHOD=kustomize PREINSTALLED=true make e2e-test  # Fast!
```

**Speed comparison**:
- Without `PREINSTALLED`: ~2-3 minutes (install + tests + uninstall)
- With `PREINSTALLED=true`: ~1 minute (tests only)

## Backward Compatibility

The implementation maintains full backward compatibility:

### Existing Behavior Unchanged
```bash
# Default behavior (no env vars) - still works
make e2e-test  # Uses helm, tests manage lifecycle

# Explicit deploy method - still works
DEPLOY_METHOD=helm make e2e-test       # ✅ Tests install via Helm
DEPLOY_METHOD=kustomize make e2e-test  # ✅ Tests install via Kustomize
```

### New Preinstalled Mode
```bash
# New flag to skip install/uninstall
DEPLOY_METHOD=helm PREINSTALLED=true make e2e-test       # Skip install, use helm webhooks
DEPLOY_METHOD=kustomize PREINSTALLED=true make e2e-test  # Skip install, use kustomize webhooks
```

## CI Integration

### GitHub Actions Example

#### Option 1: Test-Managed Operator
```yaml
- name: Run e2e tests (helm)
  run: INSTALL_METHOD=helm IMAGE_TAG=local make e2e-test
```

#### Option 2: Separate Install + Test
```yaml
- name: Install operator
  run: kubectl apply -k config/overlays/rhoai

- name: Wait for operator
  run: |
    kubectl wait --for=condition=Available \
      -n redhat-ods-applications deployment/spark-operator-controller

- name: Run e2e tests (preinstalled)
  run: INSTALL_METHOD=preinstalled make e2e-test

- name: Cleanup
  run: kubectl delete -k config/overlays/rhoai
```

## Troubleshooting

### Tests fail to find webhooks

**Error**: `timed out waiting for webhook to be ready`

**Cause**: Webhook names don't match install method

**Solution**: Verify operator is actually running and webhooks exist:
```bash
kubectl get mutatingwebhookconfigurations
kubectl get validatingwebhookconfigurations
```

### Tests try to install when using preinstalled

**Error**: `namespace already exists` or `resource already exists`

**Cause**: `INSTALL_METHOD` not set correctly

**Solution**: Ensure environment variable is set:
```bash
echo $INSTALL_METHOD  # Should show "preinstalled"
INSTALL_METHOD=preinstalled make e2e-test  # Explicit
```

### Operator not found in expected namespace

**Error**: Tests pass but SparkApplications fail to submit

**Cause**: Operator installed in different namespace than tests expect

**Note**: Tests expect operator in `spark-operator` namespace (for helm) or namespaces defined by kustomize overlays

## Implementation Details

### Code Changes

The implementation adds preinstalled support to `test/e2e/suite_test.go`:

1. **Variable rename**: `deployMethod` → `installMethod` (with backward compat)
2. **Switch cases**: Added `"preinstalled"` to all switch statements
3. **BeforeSuite**: Skip installation when `installMethod == "preinstalled"`
4. **AfterSuite**: Skip uninstallation when `installMethod == "preinstalled"`

### Key Files Modified
- `test/e2e/suite_test.go` - Main test suite setup
- `Makefile` - Updated `e2e-test` target documentation

### Lines of Code
- Total changes: ~15 lines
- New code: ~8 lines (log statements + case)
- Modified code: ~7 lines (variable names, switch cases)

## Benefits

### For Upstream Users
- ✅ Run tests on managed K8s (EKS, GKE, AKS)
- ✅ Faster development iteration (1min vs 3min per run)
- ✅ CI flexibility (separate install from testing)

### For Downstream/Midstream
- ✅ Run upstream tests on RHOAI/ODH clusters
- ✅ Eliminate test duplication (no need for separate suite_test.go)
- ✅ Test operator managed by DataScienceCluster or other controllers

### For All Users
- ✅ Backward compatible (DEPLOY_METHOD still works)
- ✅ No breaking changes
- ✅ Clear documentation and examples

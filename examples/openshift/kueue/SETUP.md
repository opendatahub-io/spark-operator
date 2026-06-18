# Kueue + SparkApplication Integration Setup on OpenShift

This guide walks you through setting up and testing the Kueue + Kubeflow Spark Operator (KSO) integration on OpenShift.

## Prerequisites

- OpenShift cluster with `oc` CLI logged in as cluster-admin
- `operator-sdk` installed (`brew install operator-sdk`)
- Spark Operator installed on the cluster (via Kustomize: `oc apply -k config/default/ --server-side=true --force-conflicts`)

## Step 1: Install RHBoK v1.4.0 (Dev Bundle)

If an older version of RHBoK is already installed, uninstall it first:

```bash
oc delete kueue cluster --ignore-not-found
oc delete subscription kueue-operator -n openshift-kueue-operator --ignore-not-found
oc delete csv kueue-operator.v1.3.1 -n openshift-kueue-operator --ignore-not-found
```

Install the v1.4.0 dev bundle (upstream Kueue v0.17+, includes SparkApplication integration):

```bash
oc create namespace openshift-kueue-operator --dry-run=client -o yaml | oc apply -f -

operator-sdk run bundle \
  quay.io/redhat-user-workloads/kueue-operator-tenant/kueue-bundle-dev-main@sha256:eae53646fd24b6c20f9807f8849bb29dd5b6b8b9566866e16e428882693c3b1e \
  --namespace openshift-kueue-operator
```

Wait for the controller manager pods to be ready:

```bash
oc get pods -n openshift-kueue-operator -w
```

You should see `kueue-controller-manager-*` pods in `Running` state.

## Step 2: Create the Kueue CR

The Kueue CR must include `SparkApplication` in the frameworks list:

```bash
oc apply -f examples/openshift/kueue/kueue-cr.yaml
```

Verify `SparkApplication` appears in the config:

```bash
oc get kueues.kueue.openshift.io cluster -o jsonpath='{.spec.config.integrations.frameworks}'
```

Wait ~30 seconds for the Kueue webhook service to become available:

```bash
oc get svc -n openshift-kueue-operator | grep webhook
```

## Step 3: Create Kueue Queue Resources

Create the ResourceFlavor, ClusterQueue (3 CPU / 3Gi quota), and LocalQueue:

```bash
oc apply -f examples/openshift/kueue/kueue-resources.yaml
```

Verify:

```bash
oc get clusterqueue spark-cq
oc get localqueue spark-lq -n spark-operator
```

## Step 4: Grant Kueue RBAC for SparkApplication

Kueue needs permission to set `blockOwnerDeletion` on Workload owner references pointing to SparkApplications. Without this, Kueue cannot create Workload objects and apps stay permanently suspended.

```bash
oc apply -f - <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kueue-sparkapplication-finalizers
rules:
- apiGroups: ["sparkoperator.k8s.io"]
  resources: ["sparkapplications/finalizers"]
  verbs: ["update"]
- apiGroups: ["sparkoperator.k8s.io"]
  resources: ["sparkapplications"]
  verbs: ["get", "list", "watch", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kueue-sparkapplication-finalizers
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kueue-sparkapplication-finalizers
subjects:
- kind: ServiceAccount
  name: kueue-controller-manager
  namespace: openshift-kueue-operator
EOF
```

## Step 5: Label the Namespace

Kueue's mutating webhook only intercepts resources in namespaces with this label:

```bash
oc label namespace spark-operator kueue.openshift.io/managed=true --overwrite
```

## Step 6: Create RBAC for Spark Driver

```bash
oc apply -f examples/openshift/k8s/base/rbac.yaml
```

## Step 7: Submit a SparkApplication

```bash
oc apply -f examples/openshift/kueue/spark-pi-kueue.yaml
```

The `kueue.x-k8s.io/queue-name: spark-lq` label tells Kueue to manage this app.

### Monitor the Lifecycle

```bash
# Watch SparkApplication status
oc get sparkapplication spark-pi-kueue -n spark-operator -w

# Check Kueue Workload objects
oc get workloads -n spark-operator

# Check pods
oc get pods -n spark-operator | grep spark-pi-kueue

# Check events
oc get events -n spark-operator --sort-by='.lastTimestamp' | tail -20
```

### Expected Lifecycle

1. Kueue webhook sets `spec.suspend=true` on creation
2. SparkApplication enters `Suspended` state
3. Kueue creates a Workload and admits it (quota available), sets `spec.suspend=false`
4. Spark Operator transitions: `Resuming` → `New` → `Submitted` → `Running`
5. Driver and executor pods are created
6. Job completes, Kueue reclaims quota

## Running the Automated E2E Tests

Automated Ginkgo tests are in `examples/openshift/kueue/kueue_test.go`. They cover:

| Test | Description |
|------|-------------|
| AC1 | Basic admission lifecycle (submit → admit → run → complete → quota reclaimed) |
| AC2 | Quota enforcement (excess jobs remain suspended while quota is exhausted) |
| AC3 | Quota reclamation (queued job admitted after completing job frees quota) |
| AC4 | Resume after suspension (suspended job runs to completion when quota freed) |

### Prerequisites

Complete steps 1-6 above.

### Run

```bash
cd /path/to/spark-operator

KUBECONFIG=$HOME/.kube/config \
go test -v -tags openshift ./examples/openshift/kueue/ \
  -ginkgo.v \
  -ginkgo.focus="Kueue SparkApplication Integration" \
  -timeout 35m
```

Tests take ~4 minutes to complete. Add `-ginkgo.v` to see step-by-step progress and SparkApplication state transitions.

## Cleanup

```bash
# Delete all SparkApplications
oc delete sparkapplication -n spark-operator --all

# Delete Kueue resources
oc delete -f examples/openshift/kueue/kueue-resources.yaml
oc delete clusterrolebinding kueue-sparkapplication-finalizers
oc delete clusterrole kueue-sparkapplication-finalizers
oc delete -f examples/openshift/kueue/kueue-cr.yaml
```

## Troubleshooting

### SparkApplications stuck in `SUSPENDED` with no Workloads created

Check Kueue controller logs for RBAC errors:

```bash
oc logs -n openshift-kueue-operator deployment/kueue-controller-manager --tail=50 | grep -i error
```

If you see `cannot set blockOwnerDeletion if an ownerReference refers to a resource you can't set finalizers on`, apply the RBAC from Step 4.

### SparkApplications not intercepted by Kueue (no `suspend=true`)

The namespace is missing the managed label. Apply it:

```bash
oc label namespace spark-operator kueue.openshift.io/managed=true --overwrite
```

### `service "spark-operator-webhook-svc" not found`

The Spark Operator is not deployed. Redeploy it:

```bash
oc apply -k config/default/ --server-side=true --force-conflicts
```

### `install plan is not available` during bundle install

An existing RHBoK subscription conflicts with the dev bundle. Uninstall the existing version first (see Step 1).

### `conversion webhook ... service "kueue-webhook-service" not found`

The Kueue controller manager hasn't finished starting. Wait ~30 seconds and retry.

### Image pull errors

The default image `ghcr.io/apache/spark-docker/spark:3.5.4` is publicly available. If you have pull rate limits, pre-pull the image or use an internal registry.

### `AccessDeniedException: /opt/spark/work-dir/...`

On OpenShift, Spark needs a writable work directory. Add an `emptyDir` volume to both driver and executor specs (already included in `spark-pi-kueue.yaml`):

```yaml
spec:
  volumes:
  - name: spark-work-dir
    emptyDir: {}
  driver:
    volumeMounts:
    - name: spark-work-dir
      mountPath: /opt/spark/work-dir
  executor:
    volumeMounts:
    - name: spark-work-dir
      mountPath: /opt/spark/work-dir
```

## Version Reference

| Component | Version | Upstream Base |
|-----------|---------|---------------|
| RHBoK | 1.4.0 | Kueue v0.17+ |
| RHBoK (previous GA) | 1.3.1 | Kueue v0.16.5 |
| Spark Operator | v2.4.0+ | kubeflow/spark-operator |
| SparkApplication integration | Alpha | Since upstream Kueue v0.17 |
| Spark image | 3.5.4 | ghcr.io/apache/spark-docker/spark |

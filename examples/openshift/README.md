# Kubeflow Spark Operator on Openshift

This documentation details how to install the Kubeflow Spark Operator (KSO) on Openshift, and goes through an example of running a Spark workload that converts documents using [docling](https://docling-project.github.io/).

## 1. Spark Operator Architecture

The Spark Operator consists of:

1.  A **SparkApplication Controller** that watches for events (Create, Update, Delete) of `SparkApplication` resources across configured namespaces.
2.  A **Submission Runner**: When a `SparkApplication` is created, the operator run the `spark-submit` command and executes it inside a simplified "submission" pod or internally.
3.  **Spark Pod Monitor**: Watches the status of the Driver and Executor pods and updates the `.status` field of the `SparkApplication` resource.
4.  **Mutating Admission Webhook**: An optional but recommended component that intercepts pod creation requests. It injects Spark-specific configuration (like mounting ConfigMaps or Volumes) into the Driver and Executor pods before they are scheduled.

KSO provides a CustomResourceDefinition called a SparkApplication which defines a Spark job, and a controller that handles the lifecycle of Spark driver and executor pods.

KSO does not run jobs on a standalone Spark cluster, rather it uses Spark's Kubernetes scheduler back end. When a SparkApplication is submitted, spark-submit points to the Kubernetes cluster and creates the spark driver pod for the application which in turn creates the executor pods. KSO expects users to bring their own Spark runtime images. KSO supports Spark 2.3 and up.

## 2. Installation on OpenShift

> **Pre-requisite:** This section requires **Cluster Admin** privileges. You must install the operator once so that users can submit `SparkApplication` CRDs.

### Prerequisites
*   OpenShift CLI (`oc`) configured
*   Cluster Admin privileges
*   Git (to clone the repository)

### Installation Steps

The operator is installed using Kustomize manifests located in `config/`.

#### 1. Clone the Repository

```bash
git clone https://github.com/opendatahub-io/spark-operator.git
cd spark-operator
```

#### 2. Login to OpenShift

Log in to your Red Hat OpenShift cluster
```bash
oc login 
```

#### 3. Install the Operator

**Option A — Using Make (recommended):**

The Makefile provides a single command that installs the operator, verifies the deployment (pods ready, non-root UID, fsGroup != 185), and optionally cleans up. Note this keeps operator running for subsequent use.
```bash
CLEANUP=false make -C examples/openshift operator-install
```

> This is the same command CI uses. If no cluster is detected, it will automatically create a local Kind cluster first.

**Option B — Manual Kustomize apply:**

```bash
oc apply -k config/default/ --server-side=true
```

> **Note:** The `--server-side=true` flag is required because the CRDs are large and exceed Kubernetes annotation size limits for client-side apply.

This creates:
- Operator namespace with controller and webhook deployments
- 3 CRDs (SparkApplication, ScheduledSparkApplication, SparkConnect)
- Comprehensive RBAC configuration
- Spark job ServiceAccount for driver pods

> **Version Note:** The Kustomize manifests use Spark Operator v2.4.0 which supports Spark 3.5.x. The document processing spark application in this guide uses Spark 3.5.7 with Java 17 for Python 3.10 compatibility (required by docling). See the [version matrix](https://github.com/kubeflow/spark-operator?tab=readme-ov-file#version-matrix) for details.
> **Multiple Namespaces:** By default, the operator watches all namespaces (empty `--namespaces=` flag). To watch specific namespaces, modify the `--namespaces` argument in `config/manager/manager.yaml` to `--namespaces=ns1,ns2,ns3`.

#### 4. Verify Installation

If you used `make operator-install`, verification is already done for you (the script checks pod readiness, non-root UID, and fsGroup). Otherwise, verify manually:

```bash
oc get pods -n spark-operator -l app.kubernetes.io/name=spark-operator
```
```text
# Expected output:
# NAME                                        READY   STATUS    RESTARTS   AGE
# spark-operator-controller-xxx               1/1     Running   0          1m
# spark-operator-webhook-xxx                  1/1     Running   0          1m
```

#### 5. Verify Security Context (OpenShift)

Confirm that the `restricted-v2` SCC is assigned:

```bash
oc describe pod -n spark-operator -l app.kubernetes.io/component=controller | grep -i openshift.io/scc
```

```text
# Expected: openshift.io/scc: restricted-v2
```

Verify the container runs with a non-root UID:

```bash
POD=$(oc get pod -n spark-operator -l app.kubernetes.io/component=controller -o jsonpath='{.items[0].metadata.name}')
oc exec -n spark-operator $POD -- id
```
```text
# Expected: uid=1000xxx gid=0(root) groups=0(root),1000xxx
```

## 3. SparkApplication CRD

The **SparkApplication** Custom Resource Definition (CRD) is the core abstraction provided by the operator. It allows you to define Spark applications declaratively using Kubernetes YAML manifests, similar to how you define Deployments or Pods.

Key fields in the `SparkApplication` spec include:

*   **`type`**: The language of the application (`Python`).
*   **`mode`**: Deployment mode (`cluster` or `client`). In `cluster` mode, the driver runs in a pod.
*   **`image`**: The container image to use for the driver and executors.
*   **`mainApplicationFile`**: The entry point path (e.g., `local:///app/scripts/run_spark_job.py`).
*   **`sparkVersion`**: The version of Spark to use (must match the image).
*   **`restartPolicy`**: Handling of failures (`Never`, `OnFailure`, `Always`).
*   **`driver` / `executor`**: Resource requests (cores, memory), labels, service accounts, and **security contexts**.
*   **`volumes` / `volumeMounts`**: PVCs for input and output data.

Here is an example snippet from `examples/openshift/k8s/docling-spark-app.yaml`:

```yaml
apiVersion: "sparkoperator.k8s.io/v1beta2"
kind: SparkApplication
metadata:
  name: docling-spark-job
  namespace: spark-operator  # One of the namespaces the operator is configured to watch
spec:
  type: Python
  pythonVersion: "3"
  mode: cluster
  image: quay.io/rishasin/docling-spark:multi-output
  imagePullPolicy: Always
  mainApplicationFile: local:///app/scripts/run_spark_job.py
  arguments:
    - "--input-dir"
    - "/app/assets"
    - "--output-file"
    - "/app/output/"
  sparkVersion: "3.5.7"
  restartPolicy:
    type: Never
  driver:
    cores: 1
    memory: "4g"
    serviceAccount: spark-operator-spark
    securityContext: {}
  executor:
    cores: 1
    instances: 2
    memory: "4g"
    securityContext: {}
```

> **Note:** See `examples/openshift/k8s/docling-spark-app.yaml` for the complete configuration 

## 4. About Docling-Spark Application

The `docling-spark` application demonstrates a production-grade pattern for processing documents at scale using:
*   **Docling**: For advanced document layout analysis and understanding.
*   **Apache Spark**: For distributed processing across the cluster.
*   **Kubeflow Spark Operator**: For native Kubernetes lifecycle management.
*   **PVC-based Storage**: Input and output data stored on persistent volumes.

### How It Works
1.  Upload PDFs to the **input PVC**.
2.  **Spark Operator** launches a Driver Pod.
3.  **Driver** reads files from input PVC, distributes work to Executor Pods.
4.  **Executors** process PDFs in parallel (OCR, Layout Analysis, Table Extraction).
5.  **Driver** collects results and writes to **output PVC**.
6.  Download results from output PVC anytime.

## 5. Deploying the Docling-Spark Application

This section uses the **pre-built image** `quay.io/rishasin/docling-spark:latest` which contains the Docling + PySpark application with all dependencies. The manifest `examples/openshift/k8s/docling-spark-app.yaml` is already configured to use this image.

> **Important:** The Spark job runs in the **same namespace** as the operator. The deploy script uses the `spark-operator-spark` ServiceAccount that is created during operator installation.

> **Building Custom Images?** If you need custom dependencies or want to use your own container registry, see [Building Custom Spark Images for OpenShift](./BuildingCustomSparkImages.md) for best practices on OpenShift compatibility and build instructions.

### Step 1: Upload Your PDFs

Make the script executable (first time only)
```bash
chmod +x examples/openshift/k8s/deploy.sh
```

# Upload your PDF files to the input PVC
```bash
./examples/openshift/k8s/deploy.sh upload ./examples/openshift/tests/assets/
```
Note, Note you can specify your own path of pdfs by passing in your own path (e.g., `./path/to/your/pdfs/`


### Step 2: Run the Spark Job
The deploy script creates PVCs and submits the SparkApplication in the operator namespace:

```bash
./examples/openshift/k8s/deploy.sh
```

Expected output:

```text
==============================================
  Deploying Docling + PySpark
==============================================
  Namespace: spark-operator
==============================================

[INFO] 1. Verifying namespace exists...
[OK] Namespace 'spark-operator' exists

[INFO] 2. Ensuring PVCs exist...
persistentvolumeclaim/docling-input created
persistentvolumeclaim/docling-output created

3. Submitting Spark Application...
sparkapplication.sparkoperator.k8s.io/docling-spark-job created

[OK] Deployment complete!

📊 Check status:
   oc get sparkapplications -n spark-operator
   oc get pods -n spark-operator -w

📝 View logs:
   oc logs -f docling-spark-job-driver -n spark-operator

🌐 Access Spark UI (when driver is running):
   oc port-forward -n spark-operator svc/docling-spark-job-ui-svc 4040:4040
   Open: http://localhost:4040

   Note: The service only exists while the SparkApplication is running
```

> **Note:** On subsequent runs, you'll see `unchanged` instead of `created` for resources that already exist.

### Step 3: Monitor the Job

#### Watch pods
Adjust namespace based on your deployment
```bash
oc get pods -n spark-operator -w
```

Expected output (pods lifecycle):

```text
NAME                                        READY   STATUS              AGE
docling-spark-job-driver                    0/1     Pending             0s
docling-spark-job-driver                    0/1     ContainerCreating   0s
docling-spark-job-driver                    1/1     Running             2s
doclingsparkjob-xxx-exec-1                  0/1     Pending             0s
doclingsparkjob-xxx-exec-1                  0/1     ContainerCreating   0s
doclingsparkjob-xxx-exec-1                  1/1     Running             3s
doclingsparkjob-xxx-exec-2                  1/1     Running             3s
...
doclingsparkjob-xxx-exec-1                  0/1     Completed           83s
doclingsparkjob-xxx-exec-2                  0/1     Completed           83s
docling-spark-job-driver                    0/1     Completed           100s
```

#### Application Logs
Once the Driver pod is created, check its logs for Spark-specific initialization and application output:

```bash
oc logs docling-spark-job-driver -n spark-operator
```
After the executor pods are created, you can view the logs via: 
```bash
oc get pods -n spark-operator \
  -l spark-role=executor \
  --field-selector=status.phase=Running \
  -o name \
| xargs -r -I{} oc logs -n spark-operator {} --all-containers=true -f
oc logs docling-spark-job-exec-1 -n spark-operator
```

#### SparkApplication Status
Inspect the status of the CRD to see if the operator encountered validation errors or submission failures:

Note: Adjust the namespace based on your deployment
```bash
oc describe sparkapplication docling-spark-job -n spark-operator
```

### Step 4: Download the Results

Note: Adjust the namespace based on your deployment
First, delete the SparkApplication to release the output PVC:
```bash
oc delete sparkapplication docling-spark-job -n spark-operator
```

Expected output:

```text
sparkapplication.sparkoperator.k8s.io "docling-spark-job" deleted
```

Download results from the output PVC:
```bash
./examples/openshift/k8s/deploy.sh download ./examples/openshift/output/
```

Expected output:

```text
==============================================
  Downloading results from Output PVC
==============================================

[INFO] Creating helper pod 'pvc-downloader'...
pod/pvc-downloader created
[INFO] Waiting for pod to be ready...
pod/pvc-downloader condition met
[OK] Helper pod ready
[INFO] Files on output PVC:
-rw-rw-r--. 1 1000840000 1000840000 2140488 Dec 15 03:55 summary.jsonl
[INFO] Copying files to './output/'...

[INFO] Downloaded files:
-rw-r--r--  1 user  staff  2140488 Dec 15 03:55 summary.jsonl
[OK] Download complete!
[INFO] Deleting helper pod 'pvc-downloader'...
pod "pvc-downloader" deleted
```

View results:
```bash
cat ./examples/openshift/output/summary.jsonl
```

## 6: Access Spark UI (Optional)

The Spark UI is only available **while the SparkApplication is actively running**. The UI service (`docling-spark-job-ui-svc`) is automatically created when the driver pod starts and deleted when the job completes.

**Prerequisites:**
- A SparkApplication must be running (driver pod in `Running` status)
- Check with: `oc get pods -n spark-operator -l spark-role=driver`

**Access the UI:**
```bash
# Adjust namespace based on your deployment
oc port-forward -n spark-operator svc/docling-spark-job-ui-svc 4040:4040
# Open: http://localhost:4040
```

## 7. Running E2E Tests with Make

The `examples/openshift/Makefile` provides standardized targets that are the same commands CI runs. These work on both **OpenShift** and local **Kind** clusters.

> **Tip:** Run `make help` from `examples/openshift/` to see all available targets and their descriptions.

### Available Make Targets

| Target | Description |
|--------|-------------|
| `make help` | Display all available targets with descriptions |
| `make kind-setup` | Create a local Kind cluster with namespace and PVCs |
| `make kind-setup-full` | Same as above + pull the docling-spark image (~9.5GB) and upload test PDFs |
| `make kind-cleanup` | Delete the Kind cluster and all resources |
| `make operator-install` | Install the Spark operator (auto-creates a Kind cluster if none detected) |
| `make test-spark-pi` | Run a lightweight Spark Pi test (auto-installs operator if needed) |
| `make test-docling-spark` | Run the Docling Spark document conversion test |
| `make test-all` | Run all tests in sequence (operator-install, spark-pi, docling) |

### Local Kind Testing (Quick Start)

If you don't have an OpenShift cluster, you can run the full test suite locally with just Docker and `kind` installed:

```bash
cd examples/openshift

# Option 1: Run everything in one command
make test-all

# Option 2: Step by step
make kind-setup                       # Create Kind cluster
CLEANUP=false make operator-install   # Install operator (keep it running)
CLEANUP=false make test-spark-pi      # Run Spark Pi test
make test-docling-spark               # Run Docling Spark test
make kind-cleanup                     # Tear down
```

> **Auto-detection:** `make operator-install` detects whether a cluster is already running. On OpenShift it uses the existing cluster; without one it creates a Kind cluster automatically.

> **Auto-dependency:** `make test-spark-pi` and `make test-docling-spark` automatically install the operator if it isn't present, so you can jump straight to a test target.

### Configuration

All test targets support the `CLEANUP` environment variable. Set `CLEANUP=false` to preserve resources between test runs (useful for debugging or chaining tests):

```bash
CLEANUP=false make test-spark-pi
```

| Variable | Default | Description |
|----------|---------|-------------|
| `CLEANUP` | `true` | Set to `false` to preserve resources after tests |
| `KIND_CLUSTER_NAME` | `spark-operator` | Name of the Kind cluster |
| `K8S_VERSION` | `v1.32.0` | Kubernetes version for Kind |
| `TIMEOUT_SECONDS` | `600` | Max wait time for test completion |

> **Detailed testing guide:** See [tests/README.md](./tests/README.md) for full documentation on individual test scripts, environment variables, architecture diagrams, and CI integration examples.

## 8. Cleanup

### Using Make (recommended for Kind clusters)

```bash
make -C examples/openshift kind-cleanup
```

### Manual Cleanup (OpenShift)

```bash
# Delete the SparkApplication
oc delete sparkapplication docling-spark-job -n spark-operator

# Delete PVCs (WARNING: This deletes all data!)
oc delete pvc docling-input docling-output -n spark-operator

# Delete the operator
oc delete -k config/default/
```

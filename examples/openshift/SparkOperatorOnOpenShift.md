# Kubeflow Spark Operator on Red Hat AI

This documentation details how the Kubeflow Spark Operator works on Red Hat AI, its architecture, installation, and how to run a distributed Spark workload using the `docling-spark` application with PVC-based storage.

## 1. Spark Operator Architecture

The Spark Operator follows the standard Kubernetes Operator pattern:

1.  **CRD Controller**: Watches for events (Create, Update, Delete) on `SparkApplication` resources across configured namespaces.
2.  **Submission Runner**: When a `SparkApplication` is created, the operator generates the `spark-submit` command and executes it inside a simplified "submission" pod or internally.
3.  **Spark Pod Monitor**: Watches the status of the Driver and Executor pods and updates the `.status` field of the `SparkApplication` resource.
4.  **Mutating Admission Webhook**: An optional but recommended component that intercepts pod creation requests. It injects Spark-specific configuration (like mounting ConfigMaps or Volumes) into the Driver and Executor pods before they are scheduled.

### Flow on OpenShift

> **Note:** The Operator must be installed by a Cluster Admin before users can submit jobs.

1.  User applies `SparkApplication` YAML.
2.  Operator Controller (running in `kubeflow-spark-operator` namespace) detects the new resource.
3.  Operator creates a **Driver Pod** in the target namespace (e.g., `docling-spark`) via the OpenShift Cluster.
4.  Driver Pod starts and requests **Executor Pods** from the OpenShift Cluster.
5.  Executor Pods start, connect to the Driver, and process the tasks.

![Spark Operator Flow on OpenShift](diagrams/Explanation_diagram.png)

## 2. Installation on OpenShift

> **Pre-requisite:** This section requires **Cluster Admin** privileges. You must install the operator once so that users can submit `SparkApplication` CRDs.

Installing the Spark Operator on OpenShift requires Helm and configuring it to work with OpenShift's `restricted-v2` Security Context Constraints (SCC).

### Prerequisites
*   OpenShift CLI (`oc`) configured.
*   Helm CLI (`helm`)
*   Cluster Admin privileges
*   Docker (for building images).
*   Quay.io account (or any container registry).

### Installation Steps

### 1. Prepare the Cluster
```bash
# Log in to your Red Hat Openshift cluster
oc login

# Install Helm (if not already installed)
brew install helm

# Add the Spark Operator Helm repo
helm repo add spark-operator https://opendatahub-io.github.io/spark-operator
helm repo update
```

### 2. Prepare Values File

We need to override some default Helm values to ensure:
1.  **Security**: The operator is compatible with OpenShift's `restricted-v2` SCC (letting OpenShift assign User IDs automatically).
2.  **Functionality**: The operator watches the `docling-spark` namespace for SparkApplications.

Create a `spark-operator-values.yaml` file (or use the one in the repo):

```yaml
controller:
  podSecurityContext:
    fsGroup: null  

webhook:
  enable: true
  podSecurityContext:
    fsGroup: null  

spark:
  jobNamespaces:
    - docling-spark
```

> **Note:** The `spark.jobNamespaces` setting specifies which namespaces the operator watches for SparkApplications. If you leave the array empty (`jobNamespaces: []`), the operator will watch **all namespaces**. For better security isolation and control, always specify the namespaces where Spark jobs should run.

> **Note:** We set `fsGroup: null` to let OpenShift's `restricted-v2` SCC assign the fsGroup automatically from the namespace's supplemental group range. The EBS CSI driver honors this and sets correct permissions on PVC mounts.

### 3. Install the Operator

First, create the required namespaces:

```bash
# Namespace where the operator runs
oc new-project kubeflow-spark-operator

# Namespace where Spark jobs will run (uses k8s/base/namespace.yaml for labels)
oc apply -f k8s/base/namespace.yaml
```

> **Note:** We use the YAML manifest for `docling-spark` to apply consistent labels (`app: docling-spark`, `environment: production`) which are useful for filtering, policies, and production environments.

**For a fresh installation:**

```bash
helm install spark-operator spark-operator/spark-operator \
    --namespace kubeflow-spark-operator \
    -f spark-operator-values.yaml \
    --version 2.2.1
```

**To update an existing installation** (e.g., to watch additional namespaces):

```bash
helm upgrade spark-operator spark-operator/spark-operator \
    --namespace kubeflow-spark-operator \
    -f spark-operator-values.yaml \
    --version 2.2.1
```

> **Tip:** Use `helm upgrade --install` to handle both cases in one command — it will install if not present, or upgrade if already installed.

> **Version Note:** We use v2.2.1 which supports Spark 3.5.x. Our application uses Spark 3.5.7 with Java 17 for Python 3.10 compatibility (required by docling). Newer versions (v2.3.x+) ship with Spark 4.x which may have breaking changes. See the [version matrix](https://github.com/kubeflow/spark-operator?tab=readme-ov-file#version-matrix) for details.

### 4. Verify Installation and Security Context
After installation, verify the operator pods are running and, crucially, that the cluster is correctly enforcing the restricted-v2 security policy required for the custom image.
1. Check Pod Status
```bash
oc get pods -n kubeflow-spark-operator
```

You should see:
- `spark-operator-controller-*` (Running)
- `spark-operator-webhook-*` (Running)

2. Confirm Security Context Constraint (SCC)
Verify that the `restricted-v2` policy is assigned to the pods:

```bash
oc describe pod <POD_NAME> -n kubeflow-spark-operator | grep -i openshift.io/scc
```

Expected output:
```
openshift.io/scc: restricted-v2
```

3. Verify Arbitrary UID Injection (Acceptance Criteria)
To definitively prove that the container is running with a random non-root UID and is a member of the required Group 0, execute the id command inside the container. This confirms the environment is ready for the compatible Spark image.
```bash
oc exec -n kubeflow-spark-operator <POD_NAME> -- id
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

> **Security Note:** The SparkApplication does NOT set `fsGroup`, `runAsUser`, or `runAsGroup`. OpenShift's `restricted-v2` SCC assigns these automatically, and the CSI driver honors them.

Example snippet from `k8s/docling-spark-app.yaml`:

```yaml
apiVersion: "sparkoperator.k8s.io/v1beta2"
kind: SparkApplication
metadata:
  name: docling-spark-job
  namespace: docling-spark
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
    - "/app/output/results.jsonl"
  sparkVersion: "3.5.7"
  restartPolicy:
    type: Never
  driver:
    cores: 1
    memory: "4g"
    serviceAccount: spark-driver
    securityContext: {}
  executor:
    cores: 1
    instances: 2
    memory: "4g"
    securityContext: {}
```

> **Note:** See `k8s/docling-spark-app.yaml` for the complete configuration including `timeToLiveSeconds` and labels.
> **Note:** To ensure compatibility with OpenShift's default restricted-v2 Security Context Constraint (SCC), the explicit securityContext block (including runAsNonRoot, fsGroup, etc.) has been removed from both the driver and executor specifications in k8s/docling-spark-app.yaml.

### Admission Control Policy

To prevent users from setting `fsGroup` in SparkApplication specs, install a ValidatingAdmissionPolicy:

```bash
# As cluster admin
oc apply -f k8s/base/validating-admission-policy.yaml
oc apply -f k8s/base/validating-admission-policy-binding.yaml
```

> Note: To Disable the policy: ```oc delete validatingadmissionpolicybinding deny-fsgroup-in-sparkapplication-binding```

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

This section uses the **pre-built image** `quay.io/rishasin/docling-spark:latest` which contains the Docling + PySpark application with all dependencies. The manifest `k8s/docling-spark-app.yaml` is already configured to use this image.

> **Building Custom Images?** If you need custom dependencies or want to use your own container registry, see [Building Custom Spark Images for OpenShift](./BuildingCustomSparkImages.md) for best practices on OpenShift compatibility and build instructions.

### Step 1: Upload Your PDFs

```bash
# Make the script executable (first time only)
chmod +x k8s/deploy.sh

# Upload your PDF files to the input PVC
./k8s/deploy.sh upload ./path/to/your/pdfs/
```


### Step 2: Run the Spark Job
The deploy script automatically creates the namespace, RBAC, and PVCs before submitting the SparkApplication:


```bash
./k8s/deploy.sh
```

Expected output:
```
==============================================
  Deploying Docling + PySpark
==============================================

[INFO] 1. Ensuring namespace exists...
namespace/docling-spark created

[INFO] 2. Creating RBAC (ServiceAccount, Role, RoleBinding)...
serviceaccount/spark-driver created
role.rbac.authorization.k8s.io/spark-role created
rolebinding.rbac.authorization.k8s.io/spark-role-binding created

[INFO] 3. Ensuring PVCs exist...
persistentvolumeclaim/docling-input created
persistentvolumeclaim/docling-output created

4. Installing ValidatingAdmissionPolicy (optional)...
   ⚠️  Skipping (requires cluster-admin). Install manually if needed.

5. Submitting Spark Application...

sparkapplication.sparkoperator.k8s.io/docling-spark-job created

[OK] Deployment complete!

📊 Check status:
   oc get sparkapplications -n docling-spark
   oc get pods -n docling-spark -w

📝 View logs:
   oc logs -f docling-spark-job-driver -n docling-spark

🌐 Access Spark UI (when driver is running):
   oc port-forward -n docling-spark svc/docling-spark-job-ui-svc 4040:4040
   Open: http://localhost:4040
```

> **Note:** On subsequent runs, you'll see `unchanged` instead of `created` for resources that already exist.

### Step 3: Monitor the Job

```bash
# Watch pods
oc get pods -n docling-spark -w
```

Expected output (pods lifecycle):
```
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

View application logs:
```bash
oc logs -f docling-spark-job-driver -n docling-spark
```

Expected output:
```
======================================================================
📄 ENHANCED PDF PROCESSING WITH PYSPARK + DOCLING
======================================================================
Creating Spark session...
Spark session created with 2 workers

📂 Step 1: Getting list of PDF files...
   Looking for PDFs in: /app/assets
✅ Found PDF: document1.pdf
✅ Found PDF: document2.pdf
   Found 2 files to process

🔄 Step 3: Processing files (this is where the magic happens!)...
   Spark is now distributing work to workers...

📊 Step 4: Organizing results...

✅ Step 5: Results are ready! (Count: 2)

💾 Step 6: Saving results to JSONL file...
✅ Results saved to: /app/output/results.jsonl

🎉 ALL DONE!
✅ Enhanced processing complete!
📦 Results are stored on the output PVC.
   To download: ./k8s/deploy.sh download ./output/
```

### Step 4: Download the Results

First, delete the SparkApplication to release the output PVC:
```bash
oc delete sparkapplication docling-spark-job -n docling-spark
```

Expected output:
```
sparkapplication.sparkoperator.k8s.io "docling-spark-job" deleted
```

Download results from the output PVC:
```bash
./k8s/deploy.sh download ./output/
```

Expected output:
```
==============================================
  Downloading results from Output PVC
==============================================

[INFO] Creating helper pod 'pvc-downloader'...
pod/pvc-downloader created
[INFO] Waiting for pod to be ready...
pod/pvc-downloader condition met
[OK] Helper pod ready
[INFO] Files on output PVC:
-rw-rw-r--. 1 1000840000 1000840000 2140488 Dec 15 03:55 results.jsonl
[INFO] Copying files to './output/'...

[INFO] Downloaded files:
-rw-r--r--  1 user  staff  2140488 Dec 15 03:55 results.jsonl
[OK] Download complete!
[INFO] Deleting helper pod 'pvc-downloader'...
pod "pvc-downloader" deleted
```

View results:
```bash
cat ./output/results.jsonl
```


### Step 5: Access Spark UI (Optional)

While the driver is running:
```bash
oc port-forward -n docling-spark svc/docling-spark-job-ui-svc 4040:4040
# Open: http://localhost:4040
```

## 6. Quick Reference

| Action | Command |
|--------|---------|
| Upload PDFs | `./k8s/deploy.sh upload ./my-pdfs/` |
| Run job | `./k8s/deploy.sh` (creates namespace, RBAC, PVCs, and submits job) |
| Check status | `./k8s/deploy.sh status` |
| View logs | `oc logs -f docling-spark-job-driver -n docling-spark` |
| Delete job | `oc delete sparkapplication docling-spark-job -n docling-spark` |
| Download results | `./k8s/deploy.sh download ./output/` |
| Cleanup helpers | `./k8s/deploy.sh cleanup` |

## 7. Debugging and Logging

### Operator Logs
If your Spark jobs are not starting (e.g., no pods created), check the operator logs:

```bash
oc logs -n kubeflow-spark-operator -l app.kubernetes.io/name=spark-operator
```

### Application Logs
Once the Driver pod is created, check its logs for Spark-specific initialization and application output:

```bash
oc logs docling-spark-job-driver -n docling-spark
oc logs docling-spark-job-exec-1 -n docling-spark
```

### SparkApplication Status
Inspect the status of the CRD to see if the operator encountered validation errors or submission failures:

```bash
oc describe sparkapplication docling-spark-job -n docling-spark
```

### CSI Driver Check
Verify the CSI driver supports fsGroup:
```bash
oc get csidriver ebs.csi.aws.com -o yaml | grep fsGroupPolicy
# Expected: fsGroupPolicy: File
```

## 8. Component Overview

| Path | Description |
|------|-------------|
| `scripts/run_spark_job.py` | PySpark driver script |
| `scripts/docling_module/` | PDF processing logic |
| `k8s/docling-spark-app.yaml` | SparkApplication manifest |
| `k8s/docling-input-pvc.yaml` | PVC for input data (10Gi default) |
| `k8s/docling-output-pvc.yaml` | PVC for output data (10Gi default) |
| `k8s/deploy.sh` | Deployment script (deploy, upload, download) |
| `Dockerfile` | Container image definition |
| `requirements-docker.txt` | Docker container dependencies |

> **Storage Note:** Both PVCs default to 10Gi. For text + metadata outputs, this is typically sufficient since output is often smaller than the source PDFs. However, if you plan to export additional artifacts (images, per-page outputs, multiple formats like JSON/MD/HTML), increase the output PVC size accordingly.

## 9. Cleanup

```bash
# Delete the SparkApplication
oc delete sparkapplication docling-spark-job -n docling-spark

# Delete PVCs (WARNING: This deletes all data!)
oc delete pvc docling-input docling-output -n docling-spark

# Delete the namespace (optional)
oc delete project docling-spark
```

### References
*   [Red Hat Developer: Raw Data to Model Serving](https://developers.redhat.com/articles/2025/07/29/raw-data-model-serving-openshift-ai)
*   [Red Hat Access: Spark Operator on OpenShift](https://access.redhat.com/articles/7131048)
*   [Kubeflow Spark Operator GitHub](https://github.com/kubeflow/spark-operator)

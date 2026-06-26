# Spark History Server with MinIO on OpenShift

Complete guide to setting up Spark History Server with MinIO storage on OpenShift. This allows you to view Spark UI for completed jobs long after the driver pods have terminated.

## Table of Contents
- [What is Spark History Server](#what-is-spark-history-server)
- [Why MinIO](#why-minio)
- [Prerequisites](#prerequisites)
- [Architecture Overview](#architecture-overview)
- [Step-by-Step Setup](#step-by-step-setup)
  - **Phase 1: Prerequisites Setup**
    - [Step 1: Build Custom Spark Image](#step-1-build-custom-spark-image-with-s3-support)
  - **Phase 2: Storage Setup**
    - [Step 2: Deploy MinIO](#step-2-deploy-minio)
    - [Step 3: Create S3 Bucket](#step-3-create-s3-bucket-in-minio)
  - **Phase 3: Application Setup**
    - [Step 4: Create Credentials Secret](#step-4-create-minio-credentials-secret)
    - [Step 5: Configure SparkApplication](#step-5-configure-sparkapplication-for-event-logging)
  - **Phase 4: History Server Setup**
    - [Step 6: Deploy History Server](#step-6-deploy-spark-history-server)
    - [Step 7: Verify and Access](#step-7-verify-and-access)
- [Understanding Event Logs](#understanding-event-logs)
- [Troubleshooting](#troubleshooting)

---

## What is Spark History Server

If you've used Spark UI before, you know it's available on the driver pod (port 4040) while your job is running. **Once the job completes and the driver pod terminates, that UI disappears.**

**Spark History Server** solves this problem:
- Spark jobs write **event logs** (detailed execution data) to persistent storage
- History Server **reads** these event logs
- History Server **reconstructs** the familiar Spark UI
- You can browse completed jobs anytime, even weeks later

**Quick Comparison:**

| | Spark UI (Live) | History Server |
|---|---|---|
| **When available?** | Only while job runs | After job completes |
| **Where?** | Driver pod :4040 | Separate deployment :18080 |
| **What happens when driver pod dies?** | UI disappears | UI persists |
| **Use case** | Monitor running jobs | Analyze completed jobs |

---

## Why MinIO

For this setup, we use **MinIO** - an S3-compatible object storage that runs inside your OpenShift cluster.

**Advantages:**
- ✅ **Self-contained** - No AWS account or external services needed
- ✅ **Reproducible** - Anyone can follow this guide without credentials
- ✅ **POC-friendly** - Spin up/tear down with your cluster
- ✅ **S3-compatible** - Same configuration works with real AWS S3 (just change endpoint)

---

## Prerequisites

Before starting, ensure you have:

### 1. OpenShift Cluster Access
```bash
oc whoami
# Should show your username
```

### 2. Spark Operator Installed
```bash
oc get pods -n spark-operator
# Should show spark-operator-controller and spark-operator-webhook pods
```

### 4. Tools Installed Locally
- `oc` CLI
- `podman` or `docker` (for building custom image)
- Access to push to a container registry (e.g., quay.io)

### 5. Storage Class
Your cluster needs a storage class for PVCs:
```bash
oc get storageclass
# Look for default storage class or note the name
```

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────┐
│ 1. SparkApplication Runs                                │
│    • Executes your Spark job                            │
│    • Writes event logs to MinIO via S3 API             │
└────────────────┬────────────────────────────────────────┘
                 │
                 │ S3 API (s3a://)
                 ▼
┌─────────────────────────────────────────────────────────┐
│ 2. MinIO (S3-Compatible Storage)                        │
│    Namespace: minio                                      │
│    • Stores event logs in bucket: spark-event-logs      │
│    • Backed by PVC (ReadWriteOnce is fine)             │
│    • Exposes S3 API on port 9000                       │
└────────────────┬────────────────────────────────────────┘
                 │
                 │ Reads event logs
                 ▼
┌─────────────────────────────────────────────────────────┐
│ 3. Spark History Server                                 │
│    Namespace: spark-test (same as SparkApplications)    │
│    • Reads logs from MinIO via S3 API                  │
│    • Reconstructs Spark UI for each job                │
│    • Serves UI on port 18080                           │
│    • Accessible via HTTPS Route                        │
└─────────────────────────────────────────────────────────┘
```

**Flow:**
1. Spark job writes event logs → MinIO (via S3A protocol)
2. History Server reads event logs ← MinIO (via S3A protocol)
3. You access History Server UI → Browse completed jobs

---

## Step-by-Step Setup

The setup is organized into three main phases:

1. **[Prerequisites Setup](#phase-1-prerequisites-setup)** - Build custom Spark image with S3 support
2. **[Storage Setup](#phase-2-storage-setup)** - Deploy MinIO and configure S3 bucket
3. **[Application Setup](#phase-3-application-setup)** - Configure Spark jobs to write event logs
4. **[History Server Setup](#phase-4-history-server-setup)** - Deploy and access History Server

---

## Phase 1: Prerequisites Setup

Build a custom Spark image with S3A libraries.

### Step 1: Build Custom Spark Image with S3 Support

The base Spark image doesn't include S3A libraries. We need to build a custom image with Hadoop AWS dependencies.

**1.1 Create Dockerfile**

Save as `Dockerfile.spark-s3`:

```dockerfile
# Dockerfile for Spark with S3/MinIO support
# Based on official Spark image with added S3A dependencies
FROM quay.io/opendatahub/data-processing:Spark-v4.0.1

LABEL maintainer="your-name"
LABEL version="4.0.1-s3"
LABEL description="Spark 4.0.1 with S3/MinIO support (hadoop-aws + aws-sdk)"

# Set the working directory
WORKDIR /opt/spark

# --- Add S3/MinIO Dependencies ---
# Download Hadoop AWS and AWS SDK JARs compatible with Spark 4.0.1
USER root

# Hadoop AWS libraries for S3A FileSystem support
# Use Hadoop 3.4.0 (already in base) with AWS SDK v2 2.25+ (has crossRegionAccessEnabled method)
RUN cd /opt/spark/jars && \
    curl -sL https://repo1.maven.org/maven2/org/apache/hadoop/hadoop-aws/3.4.0/hadoop-aws-3.4.0.jar -o hadoop-aws-3.4.0.jar && \
    curl -sL https://repo1.maven.org/maven2/software/amazon/awssdk/bundle/2.25.16/bundle-2.25.16.jar -o aws-sdk-java-bundle-2.25.16.jar && \
    curl -sL https://repo1.maven.org/maven2/software/amazon/awssdk/url-connection-client/2.25.16/url-connection-client-2.25.16.jar -o aws-url-connection-client-2.25.16.jar && \
    chgrp 0 hadoop-aws-3.4.0.jar aws-sdk-java-bundle-2.25.16.jar aws-url-connection-client-2.25.16.jar && \
    chmod 664 hadoop-aws-3.4.0.jar aws-sdk-java-bundle-2.25.16.jar aws-url-connection-client-2.25.16.jar

# --- OpenShift Arbitrary UID Compatibility ---
# OpenShift assigns arbitrary non-root UID at runtime, but it's always a member of GID 0
# All directories must be owned by group 0 and group-writable (g=u)

# Set Spark directories to be owned by group 0 and group-writable
# Required for: reading jars, writing to work-dir/logs
RUN chgrp -R 0 /opt/spark && \
    chmod -R g=u /opt/spark && \
    mkdir -p /opt/spark/work-dir /opt/spark/logs && \
    chgrp -R 0 /opt/spark/work-dir /opt/spark/logs && \
    chmod -R 775 /opt/spark/work-dir /opt/spark/logs

# Ensure /tmp is writable
RUN chmod 1777 /tmp

# Set HOME for Spark temp files
ENV HOME=/home/spark

# Create HOME directory with proper permissions
RUN mkdir -p /home/spark && \
    chgrp -R 0 /home/spark && \
    chmod -R g=u /home/spark && \
    chmod -R 775 /home/spark

# Verify JARs are present (optional - for debugging)
RUN ls -lh /opt/spark/jars/hadoop-aws* /opt/spark/jars/aws-* || echo "S3 JARs not found!"

# DO NOT set USER directive - OpenShift will assign arbitrary UID at runtime
```

**Why these JARs?**
- `hadoop-aws-3.4.0.jar` - S3A FileSystem implementation
- `aws-sdk-java-bundle-2.25.16.jar` - AWS SDK v2 (required by Hadoop 3.4.x)
- `aws-url-connection-client-2.25.16.jar` - HTTP client for AWS SDK

**1.2 Build and Push Image**

```bash
# Build the image
podman build -f Dockerfile.spark-s3 -t quay.io/YOUR_USERNAME/spark-s3:4.0.1 .

# Login to registry
podman login quay.io

# Push the image
podman push quay.io/YOUR_USERNAME/spark-s3:4.0.1

# Tag as latest
podman tag quay.io/YOUR_USERNAME/spark-s3:4.0.1 quay.io/YOUR_USERNAME/spark-s3:latest
podman push quay.io/YOUR_USERNAME/spark-s3:latest
```

**1.3 Make Image Public (if using quay.io)**

Go to https://quay.io/repository/YOUR_USERNAME/spark-s3 → Settings → Make Public

---

## Phase 2: Storage Setup

Deploy MinIO as S3-compatible storage for event logs.

### Step 2: Deploy MinIO

MinIO will run in its own namespace with its own PVC.

**2.1 Create MinIO Namespace**

```bash
oc create namespace minio
```

**2.2 Deploy MinIO**

Save as `minio-deployment.yaml`:

```yaml
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: minio-pvc
  namespace: minio
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
  storageClassName: gp3-csi  # Update to match your cluster's storage class
---
apiVersion: v1
kind: Secret
metadata:
  name: minio-credentials
  namespace: minio
stringData:
  accesskey: minioadmin
  secretkey: minioadmin123
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: minio
  namespace: minio
  labels:
    app: minio
spec:
  selector:
    matchLabels:
      app: minio
  template:
    metadata:
      labels:
        app: minio
    spec:
      containers:
      - name: minio
        image: quay.io/minio/minio:RELEASE.2024-06-13T22-53-53Z
        args:
        - server
        - /data
        env:
        - name: MINIO_ROOT_USER
          valueFrom:
            secretKeyRef:
              name: minio-credentials
              key: accesskey
        - name: MINIO_ROOT_PASSWORD
          valueFrom:
            secretKeyRef:
              name: minio-credentials
              key: secretkey
        volumeMounts:
        - name: data
          mountPath: /data
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: minio-pvc
---
apiVersion: v1
kind: Service
metadata:
  name: minio
  namespace: minio
spec:
  selector:
    app: minio
  ports:
  - port: 9000
    targetPort: 9000
```

**2.3 Apply the Configuration**

```bash
oc apply -f minio-deployment.yaml
```

**2.4 Verify MinIO is Running**

```bash
# Check pod status
oc get pods -n minio

# Should show:
# NAME                     READY   STATUS    RESTARTS   AGE
# minio-xxxxxxxxxx-xxxxx   1/1     Running   0          30s
```

---

### Step 3: Create S3 Bucket in MinIO

Spark needs a bucket to write event logs to.

**3.1 Create Bucket Using MinIO Client**

```bash
# Create bucket using mc command inside MinIO pod
oc exec -n minio deployment/minio -- \
  mc alias set local http://localhost:9000 minioadmin minioadmin123

oc exec -n minio deployment/minio -- \
  mc mb local/spark-event-logs

# Verify bucket was created
oc exec -n minio deployment/minio -- \
  mc ls local/
```

**Expected output:**
```
[2026-06-25 16:00:00 UTC]     0B spark-event-logs/
```

---

## Phase 3: Application Setup

Configure Spark jobs to write event logs to MinIO.

### Step 4: Create MinIO Credentials Secret

Spark jobs need credentials to write to MinIO. Create a secret in the namespace where you run SparkApplications.

**4.1 Create Secret**

```bash
# Replace 'spark-test' with your SparkApplication namespace
oc create namespace spark-test  # If it doesn't exist

cat <<EOF | oc apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: spark-minio-credentials
  namespace: spark-test
type: Opaque
stringData:
  AWS_ACCESS_KEY_ID: minioadmin
  AWS_SECRET_ACCESS_KEY: minioadmin123
EOF
```

**4.2 Verify Secret**

```bash
oc get secret spark-minio-credentials -n spark-test
```

---

### Step 5: Configure SparkApplication for Event Logging

Now configure your Spark jobs to write event logs to MinIO.

**5.1 Create SparkApplication with Event Logging**

Save as `spark-pi-with-eventlog.yaml`:

```yaml
apiVersion: sparkoperator.k8s.io/v1beta2
kind: SparkApplication
metadata:
  name: spark-pi-eventlog
  namespace: spark-test
spec:
  type: Scala
  mode: cluster
  image: quay.io/YOUR_USERNAME/spark-s3:4.0.1  # Your custom image
  imagePullPolicy: Always
  mainClass: org.apache.spark.examples.SparkPi
  mainApplicationFile: local:///opt/spark/examples/jars/spark-examples.jar
  arguments:
    - "1000"
  sparkVersion: "4.0.1"
  
  restartPolicy:
    type: Never
  
  sparkConf:
    # Enable event logging
    "spark.eventLog.enabled": "true"
    "spark.eventLog.dir": "s3a://spark-event-logs/"
    "spark.eventLog.compress": "true"
    
    # MinIO S3 configuration
    "spark.hadoop.fs.s3a.endpoint": "http://minio.minio.svc.cluster.local:9000"
    "spark.hadoop.fs.s3a.impl": "org.apache.hadoop.fs.s3a.S3AFileSystem"
    "spark.hadoop.fs.s3a.path.style.access": "true"
    "spark.hadoop.fs.s3a.connection.ssl.enabled": "false"
  
  driver:
    cores: 1
    memory: "1000m"
    serviceAccount: spark-operator-spark
    env:
    - name: AWS_ACCESS_KEY_ID
      valueFrom:
        secretKeyRef:
          name: spark-minio-credentials
          key: AWS_ACCESS_KEY_ID
    - name: AWS_SECRET_ACCESS_KEY
      valueFrom:
        secretKeyRef:
          name: spark-minio-credentials
          key: AWS_SECRET_ACCESS_KEY
  
  executor:
    cores: 1
    instances: 2
    memory: "1000m"
    env:
    - name: AWS_ACCESS_KEY_ID
      valueFrom:
        secretKeyRef:
          name: spark-minio-credentials
          key: AWS_ACCESS_KEY_ID
    - name: AWS_SECRET_ACCESS_KEY
      valueFrom:
        secretKeyRef:
          name: spark-minio-credentials
          key: AWS_SECRET_ACCESS_KEY
```

**Key sparkConf settings:**
- `spark.eventLog.enabled: "true"` - Enable event logging
- `spark.eventLog.dir: "s3a://spark-event-logs/"` - S3A URI to MinIO bucket  
- `spark.hadoop.fs.s3a.endpoint` - MinIO API endpoint (internal cluster DNS)

**5.2 Submit the Job**

```bash
# Update the image reference in the YAML first
oc apply -f spark-pi-with-eventlog.yaml
```

**5.3 Verify Job Completes**

```bash
# Watch job status
oc get sparkapplication spark-pi-eventlog -n spark-test -w

# Check when COMPLETED
oc get sparkapplication spark-pi-eventlog -n spark-test -o jsonpath='{.status.applicationState.state}'
```

**5.4 Verify Event Logs Were Written**

```bash
# Check MinIO storage
oc exec -n minio deployment/minio -- \
  sh -c 'ls -lh /data/spark-event-logs/'
```

**Expected output:**
```
drwxr-sr-x. 4 1000650000 1000650000 4.0K Jun 25 16:50 eventlog_v2_spark-52701ce0be984946af7a4f299e7ec6dd
drwxr-sr-x. 2 1000650000 1000650000 4.0K Jun 25 16:50 eventlog_v2_spark-52701ce0be984946af7a4f299e7ec6dd__XLDIR__
```

The event log directory contains your job's execution history!

---

## Phase 4: History Server Setup

Deploy the History Server to read event logs and provide the UI.

### Step 6: Deploy Spark History Server

Now deploy the History Server to read those event logs.

**6.1 Create History Server Deployment**

Save as `spark-history-server.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: spark-history-server
  namespace: spark-test
  labels:
    app: spark-history-server
spec:
  replicas: 1
  selector:
    matchLabels:
      app: spark-history-server
  template:
    metadata:
      labels:
        app: spark-history-server
    spec:
      serviceAccountName: spark-operator-spark
      containers:
      - name: spark-history-server
        image: quay.io/YOUR_USERNAME/spark-s3:4.0.1
        imagePullPolicy: Always
        command: ["/opt/spark/sbin/start-history-server.sh"]
        env:
        - name: AWS_ACCESS_KEY_ID
          valueFrom:
            secretKeyRef:
              name: spark-minio-credentials
              key: AWS_ACCESS_KEY_ID
        - name: AWS_SECRET_ACCESS_KEY
          valueFrom:
            secretKeyRef:
              name: spark-minio-credentials
              key: AWS_SECRET_ACCESS_KEY
        - name: SPARK_NO_DAEMONIZE
          value: "true"
        - name: SPARK_HISTORY_OPTS
          value: >-
            -Dspark.history.fs.logDirectory=s3a://spark-event-logs/
            -Dspark.hadoop.fs.s3a.endpoint=http://minio.minio.svc.cluster.local:9000
            -Dspark.hadoop.fs.s3a.impl=org.apache.hadoop.fs.s3a.S3AFileSystem
            -Dspark.hadoop.fs.s3a.path.style.access=true
            -Dspark.hadoop.fs.s3a.connection.ssl.enabled=false
            -Dspark.hadoop.fs.s3a.access.key=${AWS_ACCESS_KEY_ID}
            -Dspark.hadoop.fs.s3a.secret.key=${AWS_SECRET_ACCESS_KEY}
---
apiVersion: v1
kind: Service
metadata:
  name: spark-history-server
  namespace: spark-test
spec:
  selector:
    app: spark-history-server
  ports:
  - port: 18080
    targetPort: 18080
---
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: spark-history-server
  namespace: spark-test
  labels:
    app: spark-history-server
spec:
  port:
    targetPort: http
  to:
    kind: Service
    name: spark-history-server
  tls:
    termination: edge
    insecureEdgeTerminationPolicy: Redirect
```

**6.2 Deploy History Server**

```bash
# Update image reference in YAML first
oc apply -f spark-history-server.yaml
```

**6.3 Verify History Server is Running**

```bash
# Check pod status
oc get pods -n spark-test -l app=spark-history-server

# Should show:
# NAME                                    READY   STATUS    RESTARTS   AGE
# spark-history-server-xxxxxxxxxx-xxxxx   1/1     Running   0          20s

# Check logs
oc logs -n spark-test -l app=spark-history-server --tail=20
```

**Expected log output:**
```
Listing status of s3a://spark-event-logs/
Replaying log path: s3a://spark-event-logs/eventlog_v2_spark-...
Bound HistoryServer to 0.0.0.0, and started at http://...:18080
Started HistoryServer
```

---

### Step 7: Verify and Access

**7.1 Get History Server URL**

```bash
oc get route spark-history-server -n spark-test -o jsonpath='https://{.spec.host}'
```

**7.2 Open in Browser**

Copy the URL and open it in your browser. You should see:

- **Main page**: List of completed applications
- **Click App ID**: Full Spark UI with Jobs, Stages, Storage, Environment, Executors tabs
- **Event Timeline**: Visual representation of job execution

**7.3 Verify Your Job Appears**

You should see your `spark-pi-eventlog` application with:
- Application ID
- Application Name
- Duration
- User (service account)
- Last Updated timestamp

Click on the App ID to explore the full Spark UI!

---

## Understanding Event Logs

### What's in an Event Log?

Event logs contain **everything** that happened during your Spark job:
- Job submissions and completions
- Stage details (tasks, shuffle data, metrics)
- Executor additions and removals
- Task attempts and failures
- RDD/DataFrame caching
- SQL query plans (if using Spark SQL)

### Event Log Format

**While running:**
```
eventlog_v2_spark-<app-id>.inprogress
```

**After completion:**
```
eventlog_v2_spark-<app-id>/
├── events_1_<hash>
├── events_2_<hash>
└── appstatus_<hash>
```

Spark 4.x uses event log v2 format with rolling files for large jobs.

### Event Log Compression

With `spark.eventLog.compress: "true"`, logs are compressed using LZ4 codec by default, saving significant storage space.

---

## Troubleshooting

### Spark Job Fails with S3A Errors
Using the custom image from Step 1? Check: `oc get sparkapplication spark-pi-eventlog -n spark-test -o yaml | grep image`

### Image Build Missing JARs
Verify JARs exist: `podman run --rm quay.io/YOUR_USERNAME/spark-s3:4.0.1 ls /opt/spark/jars/hadoop-aws*`

### SDK Version Errors
Dockerfile uses AWS SDK 2.25.16+? Check Step 1.

### History Server Shows No Applications
Check event logs exist: `oc exec -n minio deployment/minio -- ls -lh /data/spark-event-logs/`
Check History Server logs: `oc logs -n spark-test -l app=spark-history-server | grep Replaying`

### MinIO Pod Not Starting
Check PVC bound: `oc get pvc -n minio minio-pvc`
Verify storage class matches your cluster: `oc get storageclass`

### Route Returns 503
Check History Server pod running: `oc get pods -n spark-test -l app=spark-history-server`

---

## Next Steps

Now that you have Spark History Server working with MinIO:

1. **Run more Spark jobs** - All jobs with event logging enabled will appear in History Server
2. **Explore the UI** - Check Jobs, Stages, Executors tabs for performance insights
3. **Customize retention** - Add log cleanup policies for production use
4. **Scale up** - Increase History Server replicas for high availability

---

**Tested With:** Spark 4.0.1, MinIO RELEASE.2024-06-13T22-53-53Z, OpenShift 4.x

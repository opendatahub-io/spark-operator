# Spark History Server with PVC Storage

Deploy Spark History Server using PersistentVolumeClaim (PVC) for event log storage. This approach works with any OpenShift cluster that has a ReadWriteMany (RWX) capable StorageClass.

## Overview

PVC storage is ideal for:
- ✅ **Disconnected/air-gapped environments** with NFS or OpenShift Data Foundation (ODF)
- ✅ **On-premises OpenShift** with enterprise storage arrays
- ✅ **Any cluster** with ReadWriteMany storage
- ✅ **Simple setup** - no external credentials or object storage required

## Storage Requirements

**For Production (Concurrent Access):**
- Requires **ReadWriteMany (RWX)** access mode
- Multiple pods can mount the PVC simultaneously
- Common RWX storage: NFS, ODF (CephFS), enterprise storage arrays

**For Testing/Demo Only:**
- Works with **ReadWriteOnce (RWO)** access mode
- Only one pod can mount at a time (sequential job execution)
- Common RWO storage: AWS EBS, Azure Disk, GCE Persistent Disk

## Quick Start

### Prerequisites

Check your available StorageClasses:

```bash
oc get storageclass
```

**Look for:**
- RWX-capable: NFS, CephFS, ODF, `ocs-storagecluster-cephfs`
- RWO-only: `gp2-csi`, `gp3-csi`, `ebs-sc`, `azure-disk`

### Step 1: Create PVC

**For Production (RWX Storage):**

Edit `spark-event-logs-pvc.yaml` to use your RWX StorageClass:

```yaml
spec:
  accessModes:
    - ReadWriteMany        # Multiple pods can mount
  storageClassName: nfs-storage  # Your RWX StorageClass
  resources:
    requests:
      storage: 20Gi
```

**For Testing (RWO Storage):**

The example uses `gp3-csi` (RWO) for demonstration:

```yaml
spec:
  accessModes:
    - ReadWriteOnce        # Only one pod at a time
  storageClassName: gp3-csi  # AWS EBS (RWO)
  resources:
    requests:
      storage: 20Gi
```

Apply the PVC:

```bash
oc apply -f spark-event-logs-pvc.yaml

# Verify it's bound
oc get pvc spark-event-logs-pvc -n spark-test
```

### Step 2: Run Spark Jobs

```bash
# Submit a job
oc apply -f spark-pi-with-eventlog.yaml

# Watch progress
oc get sparkapplication spark-pi-pvc -n spark-test -w
```

**With RWX storage:** You can run multiple jobs concurrently.

**With RWO storage:** Wait for each job to complete before submitting the next.

### Step 3: Deploy History Server

```bash
oc apply -f spark-history-server.yaml
```

**With RWX storage:** Can run alongside Spark jobs.

**With RWO storage:** Deploy only after all jobs complete and driver pods are deleted.

### Step 4: Access History Server

```bash
# Get Route URL
ROUTE=$(oc get route spark-history-server-pvc -n spark-test -o jsonpath='{.spec.host}')
echo "https://$ROUTE"
```

## Production Setup (RWX Storage)

If you have ReadWriteMany storage available:

1. **Update PVC** in `spark-event-logs-pvc.yaml`:
   ```yaml
   spec:
     accessModes:
       - ReadWriteMany
     storageClassName: nfs-storage  # or ocs-storagecluster-cephfs
   ```

2. **Deploy everything concurrently:**
   ```bash
   # Create PVC
   oc apply -f spark-event-logs-pvc.yaml
   
   # Deploy History Server
   oc apply -f spark-history-server.yaml
   
   # Run multiple Spark jobs
   oc apply -f spark-pi-with-eventlog.yaml
   ```

3. **All components work simultaneously** - no conflicts.

## Testing/Demo Setup (RWO Storage)

If you only have ReadWriteOnce storage (e.g., AWS EBS on ROSA):

1. **PVC is already configured** for `gp3-csi` (RWO)

2. **Run jobs sequentially:**
   ```bash
   # Create PVC
   oc apply -f spark-event-logs-pvc.yaml
   
   # Run ONE job at a time
   oc apply -f spark-pi-with-eventlog.yaml
   
   # Wait for completion
   oc wait --for=condition=completed sparkapplication/spark-pi-pvc -n spark-test --timeout=600s
   
   # Verify driver pod is deleted
   oc get pods -n spark-test -l spark-role=driver
   # Should show: No resources found
   
   # Now deploy History Server
   oc apply -f spark-history-server.yaml
   ```

3. **To run another job:**
   ```bash
   # Delete History Server first
   oc delete deployment spark-history-server-pvc -n spark-test
   
   # Run next job
   oc delete sparkapplication spark-pi-pvc -n spark-test
   oc apply -f spark-pi-with-eventlog.yaml
   ```

### What Happens with Concurrent Access (RWO)?

If you try to run a job while History Server is running on RWO storage:

```bash
oc describe pod spark-pi-pvc-driver -n spark-test
```

**Error:**
```
Events:
  Warning  FailedMount  Multi-Attach error for volume "pvc-xxx"
  Volume is already exclusively attached to one node and can't be attached to another
```

This demonstrates the RWO limitation.

## Files in This Example

| File | Purpose |
|------|---------|
| `spark-event-logs-pvc.yaml` | PVC for event log storage |
| `spark-pi-with-eventlog.yaml` | Example Spark job with event logging |
| `spark-history-server.yaml` | History Server deployment reading from PVC |

## Storage Comparison

| Storage Type | Concurrent Access | Setup | Cost | Best For |
|--------------|------------------|-------|------|----------|
| **PVC (RWX)** | ✅ Yes | Easy | Fixed | Disconnected, on-prem |
| **PVC (RWO)** | ❌ No | Easy | Fixed | Testing/demo only |
| **S3** | ✅ Yes | Medium | Pay-per-GB | ROSA, connected clusters |
| **MinIO** | ✅ Yes | Medium | Self-hosted | Self-hosted object storage |

## When to Use Each Storage Option

**Use PVC when:**
- Disconnected/air-gapped environment
- RWX storage already available (NFS, ODF)
- No external object storage requirements
- Simple credential-free setup preferred

**Use S3 when:**
- Running on ROSA or AWS
- Connected environment
- Want pay-as-you-go storage
- See [S3 setup guide](../s3/)

**Use MinIO when:**
- Need object storage but disconnected
- Self-hosted preference
- S3-compatible API benefits
- See [MinIO setup guide](../minio/)

## Common StorageClasses by Platform

| Platform | StorageClass | Access Mode | Notes |
|----------|--------------|-------------|-------|
| **ROSA** | `gp3-csi`, `gp2-csi` | RWO | AWS EBS - default, RWO only |
| **ROSA + EFS** | `efs-sc` | RWX | Requires EFS CSI driver addon |
| **OpenShift + ODF** | `ocs-storagecluster-cephfs` | RWX | CephFS - RWX capable |
| **On-prem + NFS** | `nfs-storage` | RWX | Common in enterprise |
| **Azure** | `managed-premium` | RWO | Azure Disk |
| **GCP** | `standard` | RWO | GCE Persistent Disk |

## Troubleshooting

### PVC Not Binding

```bash
oc describe pvc spark-event-logs-pvc -n spark-test
```

**Common causes:**
- StorageClass doesn't exist
- No available storage capacity
- Access mode not supported by StorageClass

### Multi-Attach Error (RWO Storage)

**Symptom:** Pods fail to mount with "Multi-Attach error"

**Cause:** Using ReadWriteOnce storage with multiple pods

**Solutions:**
1. Use RWX storage (production)
2. Run sequentially (testing/demo)
3. Use object storage instead (S3, MinIO)

### Event Logs Not Appearing

```bash
# Check PVC is mounted
oc exec -n spark-test deployment/spark-history-server-pvc -- ls -la /mnt/spark-event-logs/

# Should see event log files
```

**If empty:**
- Verify Spark jobs completed successfully
- Check sparkConf has correct event log path
- Ensure driver/executor pods mounted PVC

## Additional Resources

- [Comprehensive Storage Guide](../../docs/spark-history-server-setup.md) - All storage options
- [S3 Setup](../s3/) - For ROSA and connected clusters  
- [MinIO Setup](../minio/) - Self-hosted object storage
- [Spark UI Route Access](../../docs/spark-ui-route-access.md) - Live job monitoring
- [Spark UI Port-Forward](../SparkUI-PortForward.md) - Quick local access

---

**Storage Type:** PersistentVolumeClaim (PVC)  
**Production Ready:** ✅ Yes (with RWX storage) | ⚠️ No (with RWO storage)  
**Tested With:** OpenShift 4.x, Spark 3.5.7  
**Best For:** Disconnected environments, on-premises clusters with NFS/ODF

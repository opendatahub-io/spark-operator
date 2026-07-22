## Running Spark Jobs from RHOAI Workbenches

This guide walks through connecting an RHOAI workbench to a Spark Connect server so you can run Spark workloads directly from a Jupyter notebook.

### Prerequisites

- RHOAI 3.5 or newer
- DataScienceCluster (DSC) configured with both the Spark Operator and Workbenches set to Managed

### Create a Spark Connect Server

Create a SparkConnect resource in the `redhat-ods-applications` namespace:

```bash
kubectl apply -f spark-connect-server-rhoai.yaml
```

By default, the required ServiceAccount, Role, RoleBinding, and NetworkPolicy resources exist only in `redhat-ods-applications`. To deploy a SparkConnect resource in a different namespace, you must recreate these resources there. Use the following commands to inspect the originals:

```bash
oc get serviceaccount spark-operator-spark -n redhat-ods-applications -o yaml
oc get role spark-operator-role -n redhat-ods-applications -o yaml
oc get rolebinding spark-operator-rolebinding -n redhat-ods-applications -o yaml
oc get networkpolicy spark-operator-allow-internal -n redhat-ods-applications -o yaml
```

The NetworkPolicy includes a rule that allows workbenches to communicate with the Spark Connect server on port 15002:

```yaml
- from:
  - podSelector: {}
  - namespaceSelector:
      matchLabels:
        opendatahub.io/dashboard: "true"
    podSelector:
      matchLabels:
        opendatahub.io/workbenches: "true"
  ports:
  - port: 15002
    protocol: TCP
```

### Create a Service for the Spark Connect Server

The Spark Connect pod IP changes every time the pod restarts. To provide a stable endpoint, create a Service that routes traffic to the current pod automatically. This gives workbenches a fixed DNS name (`spark-connect-service.redhat-ods-applications.svc.cluster.local`) so they don't need to update their connection string after restarts.

```yaml
apiVersion: v1
kind: Service
metadata:
  name: spark-connect-service
  namespace: redhat-ods-applications
  labels:
    app.kubernetes.io/part-of: sparkoperator
spec:
  type: ClusterIP
  selector:
    sparkoperator.k8s.io/connect-name: spark-connect
    spark-role: connect-server
  ports:
    - name: spark-connect
      port: 15002
      targetPort: 15002
      protocol: TCP
```

```bash
kubectl apply -f spark-connect-service.yaml
```

### Run a Spark Workload from a Workbench

Spark Connect supports DataFrames, Spark SQL, and parts of Structured Streaming and MLlib from a Jupyter notebook. DataFrames are the best fit for notebooks since they offer a familiar tabular interface similar to pandas.

First, install PySpark and its dependencies. The PySpark version must match the Spark version used in the Spark Connect server image:

```
!pip install pyspark==4.0.1 pandas pyarrow grpcio grpcio-status zstandard
```

Then connect to the Spark Connect server and run a workload:

```python
from pyspark.sql import SparkSession

spark = SparkSession.builder.remote("sc://spark-connect-service.redhat-ods-applications.svc.cluster.local:15002").getOrCreate()

data = [("Alice", 34), ("Bob", 45), ("Claire", 66)]
df = spark.createDataFrame(data, ["Name", "Age"])
df.filter(df.Age > 40).show()
```

```
+------+---+
|  Name|Age|
+------+---+
|   Bob| 45|
|Claire| 66|
+------+---+
```

### Debugging Tips

To check the Spark Connect server logs run:

```bash
oc logs spark-connect-server -n redhat-ods-applications
```

To debug connectivity issues, open a shell on the workbench pod. First, find the pod name in the rhods-notebooks namespace, run:

```bash
oc get pods -n rhods-notebooks
```

Then exec into the pod for any other further debugging you might need to do:

```bash
oc rsh -n rhods-notebooks <workbench-pod-name>
```

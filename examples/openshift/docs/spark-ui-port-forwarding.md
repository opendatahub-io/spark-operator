# Accessing the Spark UI via Port-Forwarding

## Overview

The Spark Web UI provides real-time monitoring of Spark jobs, stages, storage,
and executors. When running SparkApplications on Kubernetes, you can access
the UI locally via `kubectl` or `oc` port-forwarding.

## Prerequisites

- A running SparkApplication (driver pod in `Running` status)
- `kubectl` or `oc` CLI configured with cluster access

## Identifying the Driver Service

The Spark Operator automatically creates a UI service for each running
SparkApplication. The service name follows the convention:

    <spark-application-name>-ui-svc

For example, a SparkApplication named `spark-pi` creates a service named
`spark-pi-ui-svc`.

### Verifying the service exists

    kubectl get svc -n <namespace> | grep ui-svc

Or check the SparkApplication status:

    kubectl get sparkapplication <name> -n <namespace> -o jsonpath='{.status.driverInfo.webUIServiceName}'

## Port-Forward Command

    kubectl port-forward -n <namespace> svc/<app-name>-ui-svc 4040:4040

Or using `oc` on OpenShift:

    oc port-forward -n <namespace> svc/<app-name>-ui-svc 4040:4040

Then open: http://localhost:4040

> **Security note:** `port-forward` binds to `localhost` by default. Avoid using
> `--address 0.0.0.0` on shared networks, as the Spark UI can expose job and
> environment metadata.

### Alternate: Port-forward directly to the driver pod

    kubectl port-forward -n <namespace> <app-name>-driver 4040:4040

## When to Use Port-Forwarding vs Routes/Ingress

| Method | Use Case | Accessibility |
|--------|----------|---------------|
| Port-forward | Local dev/testing, debugging | localhost only |
| Route (OpenShift) / Ingress | Production, shared access | Cluster-wide URL |

## Limitations

- **Local only** — accessible only from the machine running the port-forward
- **Requires an active terminal** — the session ends when the terminal closes
- **Ephemeral** — the UI service only exists while the SparkApplication is running;
  once the job completes, the service is deleted
- **Custom ports** — if `spark.ui.port` is set in sparkConf, replace `4040`
  with that port value

## Troubleshooting

- **Service not found?** Ensure the SparkApplication is in `Running` state.
  The UI service is created when the driver starts and removed on completion.
- **Port conflict?** Use a different local port:
  `kubectl port-forward -n <namespace> svc/<name>-ui-svc 8080:4040` then browse to
  http://localhost:8080
- **Connection refused?** The driver pod may not be fully ready yet. Wait and retry.

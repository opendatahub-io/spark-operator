# Spark Operator Module

Thin deployment orchestrator scaffold for the Spark workload operator on Open Data Hub / RHOAI platforms.

## Build

From the repository root:

```bash
make generate-spark-operator-module manifests-spark-operator-module
cd spark-operator-module && go build ./...
make kustomize-build-spark-operator-module
```

# Spark Operator Upgrade Tests

## Overview

Upgrade tests are located in https://github.com/opendatahub-io/opendatahub-tests/tree/main/tests/spark/upgrade and are written in Python.

These test are registered in CI via jenkins piepeline here:https://gitlab.cee.redhat.com/ods/jenkins/-/blob/master/resources/configs/components-testing/components/spark-operator-upgrade/main.yaml

These tests verify that the Spark Operator and its workloads survive an OpenShift AI upgrade. They run in two phases against a shared `upgrade-spark-operator` namespace.

Currently SparkApplications have upgrade test coverage, but SparkConnects does not.

Upgrade tests will be maintained by the rhai-data-processing team

## Upgrade Test Overview

### Pre-Upgrade

Before the upgrade to the spark-operator is done the following happens:

1. Enables the Spark Operator by patching the DataScienceCluster (`sparkoperator.managementState: Managed`)
2. Discovers RBAC resources (Role, RoleBinding, ServiceAccount) and NetworkPolicies from the applications namespace and recreates them in the test namespace
3. Deploys a `spark-pi` SparkApplication and waits for it to complete
4. Captures a baseline (resource generation, pod restart counts, application state) into a ConfigMap

### Post-Upgrade

After the upgrade to the spark-operator is done the following is tested:

1. Loads the baseline from the ConfigMap
2. Verifies the pre-existing SparkApplication:
   - Still exists
   - `metadata.generation` unchanged (resource was not modified)
   - Pods did not restart
   - Still in `COMPLETED` state
3. Creates a new SparkApplication and verifies it runs to completion on the upgraded operator
4. Restores the Spark Operator to `Removed` state

## Operator Chaos Maturity Level

The current operator-chaos maturity level is L1.

Details on the workflow for the operator-chaos test is at: https://github.com/opendatahub-io/spark-operator/blob/main/.github/workflows/operator-chaos.yaml

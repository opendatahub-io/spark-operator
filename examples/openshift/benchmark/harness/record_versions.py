#!/usr/bin/env python3
"""Record OpenShift / RHOAI / Spark Operator versions and cluster topology."""

from __future__ import annotations

import json
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Optional


def run(cmd: list[str]) -> str:
    try:
        out = subprocess.check_output(cmd, stderr=subprocess.STDOUT, text=True)
        return out.strip()
    except subprocess.CalledProcessError as e:
        return f"ERROR: {e.output.strip()}"
    except FileNotFoundError:
        return "ERROR: command not found"


def jsonpath(resource: str, path: str, namespace: Optional[str] = None) -> str:
    cmd = ["oc", "get", resource]
    if namespace:
        cmd += ["-n", namespace]
    cmd += ["-o", f"jsonpath={path}"]
    return run(cmd)


def main() -> int:
    output = Path(sys.argv[1]) if len(sys.argv) > 1 else Path("versions.json")

    workers_json = run(
        [
            "oc",
            "get",
            "nodes",
            "-l",
            "node-role.kubernetes.io/worker",
            "-o",
            "json",
        ]
    )
    worker_count = 0
    instance_types: dict[str, int] = {}
    try:
        import json as _json

        data = _json.loads(workers_json)
        items = data.get("items", [])
        worker_count = len(items)
        for item in items:
            itype = (
                item.get("metadata", {})
                .get("labels", {})
                .get("node.kubernetes.io/instance-type", "unknown")
            )
            instance_types[itype] = instance_types.get(itype, 0) + 1
    except Exception:
        pass

    payload: dict[str, Any] = {
        "recorded_at": datetime.now(timezone.utc).isoformat(),
        "openshift_version": run(
            [
                "oc",
                "get",
                "clusterversion",
                "version",
                "-o",
                "jsonpath={.status.desired.version}",
            ]
        ),
        "kubernetes_version": run(["oc", "version", "-o", "json"]),
        "rhoai_version": jsonpath(
            "sparkoperator/default-sparkoperator",
            "{.metadata.annotations.platform\\.opendatahub\\.io/version}",
            namespace="opendatahub",
        ),
        "spark_operator_release": jsonpath(
            "sparkoperator/default-sparkoperator",
            "{.status.releases[?(@.name=='Spark Operator')].version}",
            namespace="opendatahub",
        ),
        "spark_release": jsonpath(
            "sparkoperator/default-sparkoperator",
            "{.status.releases[?(@.name=='Spark')].version}",
            namespace="opendatahub",
        ),
        "spark_operator_image": jsonpath(
            "deployment/spark-operator-controller",
            "{.spec.template.spec.containers[0].image}",
            namespace="redhat-ods-applications",
        ),
        "controller_threads": run(
            [
                "oc",
                "get",
                "deployment",
                "spark-operator-controller",
                "-n",
                "redhat-ods-applications",
                "-o",
                "jsonpath={.spec.template.spec.containers[0].args}",
            ]
        ),
        "cluster_topology": {
            "worker_count": worker_count,
            "worker_instance_types": instance_types,
        },
        "webhook_inventory": {
            "mutating": run(
                [
                    "oc",
                    "get",
                    "mutatingwebhookconfiguration",
                    "mutating-webhook-configuration",
                    "-o",
                    "json",
                ]
            ),
            "note": "Parse webhook namespaceSelectors before the run; stock RHOAI may only match 'default'.",
        },
    }

    # kubernetes version may be large JSON; keep server version string if possible
    try:
        kv = json.loads(payload["kubernetes_version"])
        payload["kubernetes_version"] = kv.get("serverVersion", {}).get(
            "gitVersion", payload["kubernetes_version"]
        )
    except Exception:
        pass

    output.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    print(f"Wrote {output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

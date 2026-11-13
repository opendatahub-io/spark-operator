#!/usr/bin/env python3
"""Snapshot node pod limits, CPU, memory, pod IPs, ResourceQuota, namespace limits, admission observations (SCC / NetworkPolicy
event hits, or explicit \"none observed\").
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Optional

import yaml


def _oc(*args: str) -> str:
    try:
        return subprocess.check_output(
            ["oc", *args], stderr=subprocess.STDOUT, text=True
        ).strip()
    except (subprocess.CalledProcessError, FileNotFoundError) as e:
        detail = getattr(e, "output", str(e))
        return f"ERROR: {detail}"


def _oc_json(*args: str) -> Optional[Any]:
    raw = _oc(*args, "-o", "json")
    if raw.startswith("ERROR:"):
        return None
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return None


def _ki_to_mi(value: str) -> Optional[float]:
    if not value:
        return None
    if value.endswith("Ki"):
        return round(int(value[:-2]) / 1024.0, 1)
    if value.endswith("Mi"):
        return float(value[:-2])
    if value.endswith("Gi"):
        return float(value[:-2]) * 1024.0
    try:
        return round(int(value) / (1024 * 1024), 1)
    except ValueError:
        return None


def _cpu_m(value: Any) -> int:
    text = str(value or "0").strip()
    match = re.fullmatch(r"([0-9]+(?:\.[0-9]+)?)(n|u|m)?", text)
    if not match:
        raise ValueError(f"unsupported CPU quantity: {value}")
    amount = float(match.group(1))
    suffix = match.group(2)
    if suffix == "n":
        return round(amount / 1_000_000)
    if suffix == "u":
        return round(amount / 1_000)
    if suffix == "m":
        return round(amount)
    return round(amount * 1000)


def _node_is_eligible(node: dict[str, Any]) -> bool:
    if node.get("spec", {}).get("unschedulable"):
        return False
    ready = any(
        condition.get("type") == "Ready" and condition.get("status") == "True"
        for condition in node.get("status", {}).get("conditions", []) or []
    )
    blocked = any(
        taint.get("effect") in {"NoSchedule", "NoExecute"}
        for taint in node.get("spec", {}).get("taints", []) or []
    )
    return ready and not blocked


def _container_cpu_request_m(containers: list[dict[str, Any]]) -> int:
    return sum(
        _cpu_m(container.get("resources", {}).get("requests", {}).get("cpu", "0"))
        for container in containers
    )


def _pod_cpu_request_m(pod: dict[str, Any]) -> int:
    spec = pod.get("spec", {})
    regular = _container_cpu_request_m(spec.get("containers", []) or [])
    init = max(
        (
            _container_cpu_request_m([container])
            for container in spec.get("initContainers", []) or []
        ),
        default=0,
    )
    return max(regular, init) + _cpu_m((spec.get("overhead") or {}).get("cpu", "0"))


def _workload_requirements(template_path: Path, applications: int) -> dict[str, Any]:
    template = yaml.safe_load(template_path.read_text(encoding="utf-8"))
    if not isinstance(template, dict):
        raise ValueError("workload template must be a YAML mapping")
    spec = template.get("spec") or {}
    driver = spec.get("driver") or {}
    executor = spec.get("executor") or {}
    executor_count = int(executor.get("instances", 1))
    driver_cpu_m = _cpu_m(driver.get("coreRequest", driver.get("cores", 1)))
    executor_cpu_m = _cpu_m(executor.get("coreRequest", executor.get("cores", 1)))
    cpu_per_application_m = driver_cpu_m + executor_count * executor_cpu_m
    pods_per_application = 1 + executor_count
    return {
        "applications": applications,
        "image": spec.get("image"),
        "image_pull_policy": spec.get("imagePullPolicy", "IfNotPresent"),
        "cpu_per_application_m": cpu_per_application_m,
        "pods_per_application": pods_per_application,
        "required_cpu_m": applications * cpu_per_application_m,
        "required_pods": applications * pods_per_application,
    }


def _qualify_capacity(
    available_cpu_m: int,
    available_pods: int,
    required: dict[str, int],
    minimum_ratio: float = 0.5,
) -> dict[str, Any]:
    ratios = {
        "cpu": available_cpu_m / required["required_cpu_m"],
        "pods": available_pods / required["required_pods"],
    }
    checks = {
        resource: ratio >= minimum_ratio for resource, ratio in ratios.items()
    }
    return {
        "minimum_capacity_ratio": minimum_ratio,
        "available_to_required_ratio": ratios,
        "passed": all(checks.values()),
        "checks": checks,
    }


def _event_hits(
    namespaces: list[str], substrings: list[str], limit: int = 10
) -> dict[str, Any]:
    """Scan recent namespace events; return status + samples for ticket ACs."""
    samples: list[str] = []
    for ns in namespaces:
        data = _oc_json("get", "events", "-n", ns)
        if not data:
            continue
        items = list(reversed(data.get("items") or []))
        for item in items:
            blob = f"{item.get('reason') or ''}: {item.get('message') or ''}"
            if any(s.lower() in blob.lower() for s in substrings):
                samples.append(f"[{ns}] {blob}")
                if len(samples) >= limit:
                    return {"status": "observed", "samples": samples}
    if samples:
        return {"status": "observed", "samples": samples}
    return {"status": "none observed", "samples": []}


def collect(
    namespaces: list[str],
    template_path: Optional[Path] = None,
    applications: Optional[int] = None,
) -> dict[str, Any]:
    nodes = _oc_json("get", "nodes")
    if nodes is None:
        raise RuntimeError("failed to list nodes")
    node_rows = []
    eligible_nodes: set[str] = set()
    sum_cpu_m = 0
    sum_mem_mi = 0.0
    sum_pods = 0
    for item in nodes.get("items") or []:
        meta = item.get("metadata", {})
        status = item.get("status", {})
        alloc = status.get("allocatable", {})
        labels = meta.get("labels", {})
        eligible = _node_is_eligible(item)
        cpu_m = _cpu_m(alloc.get("cpu", "0"))
        mem_mi = _ki_to_mi(alloc.get("memory", "0")) or 0.0
        pods = int(alloc.get("pods", "0"))
        if eligible:
            eligible_nodes.add(meta.get("name"))
            sum_cpu_m += cpu_m
            sum_mem_mi += mem_mi
            sum_pods += pods
        node_rows.append(
            {
                "name": meta.get("name"),
                "eligible": eligible,
                "instance_type": labels.get(
                    "node.kubernetes.io/instance-type", "unknown"
                ),
                "allocatable_cpu_m": cpu_m,
                "allocatable_memory_mi": mem_mi,
                "allocatable_pods": pods,
            }
        )

    pods = _oc_json("get", "pods", "--all-namespaces")
    if pods is None:
        raise RuntimeError("failed to list pods")
    requested_cpu_m = 0
    active_pods = 0
    for pod in pods.get("items") or []:
        if pod.get("status", {}).get("phase") in {"Succeeded", "Failed"}:
            continue
        if pod.get("spec", {}).get("nodeName") not in eligible_nodes:
            continue
        requested_cpu_m += _pod_cpu_request_m(pod)
        active_pods += 1

    available_cpu_m = max(0, sum_cpu_m - requested_cpu_m)
    available_pods = max(0, sum_pods - active_pods)

    network = _oc_json("get", "network.config", "cluster") or {}
    net_spec = network.get("spec") or network.get("status") or {}
    cluster_networks = net_spec.get("clusterNetwork") or []

    quotas = []
    limit_ranges = []
    for ns in namespaces:
        ns = ns.strip()
        if not ns:
            continue
        q = _oc_json("get", "resourcequota", "-n", ns)
        if q and q.get("items"):
            for item in q["items"]:
                quotas.append(
                    {
                        "namespace": ns,
                        "name": item.get("metadata", {}).get("name"),
                        "hard": (item.get("status") or {}).get("hard")
                        or (item.get("spec") or {}).get("hard"),
                        "used": (item.get("status") or {}).get("used"),
                    }
                )
        else:
            quotas.append({"namespace": ns, "name": None, "hard": None, "used": None})

        lr = _oc_json("get", "limitrange", "-n", ns)
        if lr and lr.get("items"):
            for item in lr["items"]:
                limit_ranges.append(
                    {
                        "namespace": ns,
                        "name": item.get("metadata", {}).get("name"),
                        "limits": item.get("spec", {}).get("limits"),
                    }
                )
        else:
            limit_ranges.append({"namespace": ns, "name": None, "limits": None})

    host_prefix = None
    if cluster_networks:
        host_prefix = cluster_networks[0].get("hostPrefix")

    payload: dict[str, Any] = {
        "recorded_at": datetime.now(timezone.utc).isoformat(),
        "nodes": node_rows,
        "totals": {
            "allocatable_cpu_m": sum_cpu_m,
            "allocatable_memory_mi": round(sum_mem_mi, 1),
            "allocatable_pods": sum_pods,
            "requested_cpu_m": requested_cpu_m,
            "active_pods": active_pods,
            "available_cpu_m": available_cpu_m,
            "available_pods": available_pods,
        },
        "network": {
            "networkType": net_spec.get("networkType"),
            "clusterNetwork": cluster_networks,
            "hostPrefix": host_prefix,
            "pod_ip_note": (
                f"hostPrefix={host_prefix} yields about {2 ** (32 - int(host_prefix)) if host_prefix else '?'} "
                "addresses per node; compare to allocatable_pods. "
                "On this cluster maxPods is usually the tighter limit."
            ),
        },
        "resourceQuotas": quotas,
        "limitRanges": limit_ranges,
        "scc_admission_failures": _event_hits(
            namespaces,
            [
                "SCC",
                "violates PodSecurity",
                "unable to validate against any security",
            ],
        ),
        "networkpolicy_related_failures": _event_hits(
            namespaces,
            ["NetworkPolicy", "network policy", "blocked by network"],
        ),
    }

    if template_path is not None and applications is not None:
        workload = _workload_requirements(template_path, applications)
        payload["workload"] = workload
        payload["qualification"] = _qualify_capacity(
            available_cpu_m, available_pods, workload
        )

    return payload


def _print_qualification(payload: dict[str, Any]) -> None:
    qualification = payload.get("qualification")
    if qualification is None:
        return
    workload = payload["workload"]
    totals = payload["totals"]
    rows = (
        ("CPU", workload["required_cpu_m"], totals["available_cpu_m"], "m", "cpu"),
        ("Pods", workload["required_pods"], totals["available_pods"], "", "pods"),
    )
    threshold = qualification["minimum_capacity_ratio"]
    print(f"Benchmark capacity qualification (threshold: {threshold:.0%} of requested workload)")
    print(
        f"{'Resource':<10} {'Required':>12} {'Available':>12} "
        f"{'Ratio':>8} {'Result':>8}"
    )
    for label, required, available, suffix, check in rows:
        ratio = qualification["available_to_required_ratio"][check]
        result = "PASS" if qualification["checks"][check] else "FAIL"
        print(
            f"{label:<10} {str(required) + suffix:>12} "
            f"{str(available) + suffix:>12} {ratio:>7.1%} {result:>8}"
        )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("output", nargs="?", default="capacity.json")
    parser.add_argument("namespaces", nargs="?", default="")
    parser.add_argument("--template", type=Path)
    parser.add_argument("--applications", type=int)
    parser.add_argument("--ack-low-capacity", action="store_true")
    args = parser.parse_args()
    if (args.template is None) != (args.applications is None):
        parser.error("--template and --applications must be provided together")
    if args.applications is not None and args.applications < 1:
        parser.error("--applications must be positive")

    namespaces = [n.strip() for n in args.namespaces.split(",") if n.strip()]
    try:
        payload = collect(namespaces, args.template, args.applications)
    except (OSError, RuntimeError, ValueError, yaml.YAMLError) as exc:
        print(f"ERROR: capacity snapshot failed: {exc}", file=sys.stderr)
        return 1
    qualification = payload.get("qualification")
    if qualification is not None:
        qualification["low_capacity_acknowledged"] = args.ack_low_capacity

    output = Path(args.output)
    output.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    totals = payload["totals"]
    print(
        f"Wrote {output} "
        f"(cpu_m={totals['allocatable_cpu_m']}, "
        f"mem_mi={totals['allocatable_memory_mi']}, "
        f"pods={totals['allocatable_pods']})"
    )
    _print_qualification(payload)
    if qualification is not None and not qualification["passed"]:
        if args.ack_low_capacity:
            print("WARNING: continuing because ACK_LOW_CAPACITY=1 was provided.")
            return 0
        print(
            "ERROR: insufficient free capacity for this benchmark; "
            "set ACK_LOW_CAPACITY=1 to override deliberately.",
            file=sys.stderr,
        )
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

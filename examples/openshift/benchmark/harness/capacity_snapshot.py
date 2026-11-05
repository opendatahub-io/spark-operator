#!/usr/bin/env python3
"""Snapshot node pod limits, CPU, memory, pod IPs, ResourceQuota, namespace limits, admission observations (SCC / NetworkPolicy
event hits, or explicit \"none observed\").
"""

from __future__ import annotations

import json
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Optional


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


def collect(namespaces: list[str]) -> dict[str, Any]:
    nodes = _oc_json("get", "nodes") or {"items": []}
    node_rows = []
    sum_cpu_m = 0
    sum_mem_mi = 0.0
    sum_pods = 0
    for item in nodes.get("items") or []:
        meta = item.get("metadata", {})
        status = item.get("status", {})
        alloc = status.get("allocatable", {})
        labels = meta.get("labels", {})
        cpu = alloc.get("cpu", "0")
        if cpu.endswith("m"):
            cpu_m = int(cpu[:-1])
        else:
            cpu_m = int(float(cpu) * 1000)
        mem_mi = _ki_to_mi(alloc.get("memory", "0")) or 0.0
        pods = int(alloc.get("pods", "0"))
        sum_cpu_m += cpu_m
        sum_mem_mi += mem_mi
        sum_pods += pods
        node_rows.append(
            {
                "name": meta.get("name"),
                "instance_type": labels.get(
                    "node.kubernetes.io/instance-type", "unknown"
                ),
                "allocatable_cpu_m": cpu_m,
                "allocatable_memory_mi": mem_mi,
                "allocatable_pods": pods,
            }
        )

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

    return {
        "recorded_at": datetime.now(timezone.utc).isoformat(),
        "nodes": node_rows,
        "totals": {
            "allocatable_cpu_m": sum_cpu_m,
            "allocatable_memory_mi": round(sum_mem_mi, 1),
            "allocatable_pods": sum_pods,
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
        "workload_pod_formula": {
            "pods_per_application": 6,
            "note": "applications × (1 driver + 5 executors); see RHAIENG-7483-capacity-note.md",
        },
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


def main() -> int:
    output = Path(sys.argv[1]) if len(sys.argv) > 1 else Path("capacity.json")
    namespaces = []
    if len(sys.argv) > 2:
        namespaces = [n.strip() for n in sys.argv[2].split(",") if n.strip()]
    payload = collect(namespaces)
    output.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    totals = payload["totals"]
    print(
        f"Wrote {output} "
        f"(cpu_m={totals['allocatable_cpu_m']}, "
        f"mem_mi={totals['allocatable_memory_mi']}, "
        f"pods={totals['allocatable_pods']})"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

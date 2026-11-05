#!/usr/bin/env python3
"""Write a live admission-webhook inventory for SparkApplication and Spark pod rules.

Does not hard-code webhook counts. Reads MutatingWebhookConfiguration and
ValidatingWebhookConfiguration from the cluster under test and keeps only
entries whose rules match SparkApplication CREATE/UPDATE or Pod CREATE.
"""

from __future__ import annotations

import json
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Optional


def _oc_json(resource: str) -> Optional[dict[str, Any]]:
    try:
        out = subprocess.check_output(
            ["oc", "get", resource, "-o", "json"],
            stderr=subprocess.STDOUT,
            text=True,
        )
        return json.loads(out)
    except (subprocess.CalledProcessError, FileNotFoundError, json.JSONDecodeError):
        return None


def _ops(rule: dict[str, Any]) -> set[str]:
    return {str(o).upper() for o in rule.get("operations") or []}


def _resources(rule: dict[str, Any]) -> set[str]:
    return {str(r).lower() for r in rule.get("resources") or []}


def _api_groups(rule: dict[str, Any]) -> set[str]:
    return {str(g) for g in rule.get("apiGroups") or []}


def _matches_sparkapplication(rule: dict[str, Any]) -> bool:
    if "sparkoperator.k8s.io" not in _api_groups(rule):
        return False
    if "sparkapplications" not in _resources(rule):
        return False
    ops = _ops(rule)
    return bool(ops & {"CREATE", "UPDATE", "*"})


def _matches_pod_create(rule: dict[str, Any]) -> bool:
    groups = _api_groups(rule)
    if groups and "" not in groups and "*" not in groups:
        return False
    if "pods" not in _resources(rule) and "*" not in _resources(rule):
        return False
    ops = _ops(rule)
    return bool(ops & {"CREATE", "*"})


def _ns_selector(webhook: dict[str, Any]) -> Any:
    return webhook.get("namespaceSelector") or {}


def _summarize_webhook(kind: str, config_name: str, webhook: dict[str, Any]) -> dict[str, Any]:
    rules = webhook.get("rules") or []
    match_sa = any(_matches_sparkapplication(r) for r in rules)
    match_pod = any(_matches_pod_create(r) for r in rules)
    name = webhook.get("name", "")
    spark_operator = "sparkoperator.k8s.io" in name
    return {
        "kind": kind,
        "config": config_name,
        "name": name,
        "matches_sparkapplication_create_update": match_sa,
        "matches_pod_create": match_pod,
        "spark_operator_webhook": spark_operator,
        "failurePolicy": webhook.get("failurePolicy"),
        "timeoutSeconds": webhook.get("timeoutSeconds"),
        "namespaceSelector": _ns_selector(webhook),
        "rules": [
            {
                "apiGroups": r.get("apiGroups"),
                "apiVersions": r.get("apiVersions"),
                "operations": r.get("operations"),
                "resources": r.get("resources"),
                "scope": r.get("scope"),
            }
            for r in rules
            if _matches_sparkapplication(r) or _matches_pod_create(r)
        ],
    }


def collect() -> dict[str, Any]:
    entries: list[dict[str, Any]] = []
    for kind, resource in (
        ("MutatingWebhookConfiguration", "mutatingwebhookconfigurations"),
        ("ValidatingWebhookConfiguration", "validatingwebhookconfigurations"),
    ):
        listing = _oc_json(resource)
        if not listing:
            continue
        for item in listing.get("items") or []:
            config_name = item.get("metadata", {}).get("name", "")
            for webhook in item.get("webhooks") or []:
                rules = webhook.get("rules") or []
                if any(
                    _matches_sparkapplication(r) or _matches_pod_create(r) for r in rules
                ):
                    entries.append(_summarize_webhook(kind, config_name, webhook))

    spark_ops = [e for e in entries if e["spark_operator_webhook"]]
    platform = [e for e in entries if not e["spark_operator_webhook"]]
    return {
        "recorded_at": datetime.now(timezone.utc).isoformat(),
        "matching_entry_count": len(entries),
        "spark_operator_entry_count": len(spark_ops),
        "other_matching_entry_count": len(platform),
        "spark_operator_webhooks": spark_ops,
        "other_matching_webhooks": platform,
        "note": (
            "Counts come from the live cluster. Measure Spark Operator webhook "
            "latency with apiserver_admission_webhook_* filtered by name "
            "containing sparkoperator.k8s.io; do not mix with other_matching_webhooks."
        ),
    }


def main() -> int:
    output = Path(sys.argv[1]) if len(sys.argv) > 1 else Path("webhook-inventory.json")
    payload = collect()
    output.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    print(
        f"Wrote {output} "
        f"(spark_operator={payload['spark_operator_entry_count']}, "
        f"other={payload['other_matching_entry_count']})"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

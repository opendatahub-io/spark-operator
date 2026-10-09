#!/usr/bin/env python3
"""Run the P0 PromQL catalog against the OpenShift Thanos querier."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import urllib.parse
import urllib.request
import ssl
from datetime import datetime, timezone
from pathlib import Path

import yaml


def _oc(*args: str) -> str:
    result = subprocess.run(
        ["oc", *args],
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        detail = (result.stderr or result.stdout or "").strip()
        raise RuntimeError(f"oc {' '.join(args)} failed: {detail}")
    return result.stdout.strip()


def _env_truthy(name: str, default: str = "false") -> bool:
    return os.environ.get(name, default).strip().lower() in {"1", "true", "yes", "y"}


def _tls_context() -> ssl.SSLContext:
    """Build a verified context for the Thanos querier route.

    The route certificate is signed by the cluster's default ingress CA,
    published in openshift-config-managed/default-ingress-cert and absent
    from the host trust store (macOS Python ships no CA bundle), so trust
    exactly that CA instead of disabling verification.
    """
    if _env_truthy("SPARK_BENCH_INSECURE_SKIP_TLS_VERIFY", "false"):
        # Same opt-in escape hatch as harness/k8s_client.py, for setups
        # where the CA bundle cannot be read.
        context = ssl.create_default_context()
        context.check_hostname = False  # tls-lint:ignore
        context.verify_mode = ssl.CERT_NONE  # tls-lint:ignore
        return context

    ca_bundle = _oc(
        "get", "configmap", "default-ingress-cert",
        "-n", "openshift-config-managed",
        "-o", "jsonpath={.data.ca-bundle\\.crt}",
    )
    if "BEGIN CERTIFICATE" not in ca_bundle:
        raise RuntimeError(
            "Could not read the cluster ingress CA bundle "
            "(configmap/default-ingress-cert in openshift-config-managed); "
            "cannot verify the Thanos querier route certificate. Re-run with "
            "SPARK_BENCH_INSECURE_SKIP_TLS_VERIFY=true to skip verification."
        )
    return ssl.create_default_context(cadata=ca_bundle)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--queries", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()

    catalog = yaml.safe_load(Path(args.queries).read_text(encoding="utf-8"))
    host = _oc(
        "get", "route", "thanos-querier",
        "-n", "openshift-monitoring",
        "-o", "jsonpath={.spec.host}",
    )
    token = _oc("whoami", "-t")

    context = _tls_context()
    results = []
    for item in catalog["queries"]:
        expr = item["expr"].strip()
        url = (
            f"https://{host}/api/v1/query?"
            + urllib.parse.urlencode({"query": expr})
        )
        request = urllib.request.Request(
            url, headers={"Authorization": f"Bearer {token}"}
        )
        entry = {
            "id": item["id"],
            "group": item["group"],
            "expr": expr,
        }
        try:
            with urllib.request.urlopen(request, context=context, timeout=30) as response:
                body = json.loads(response.read().decode("utf-8"))
            entry["status"] = body.get("status")
            entry["result"] = body.get("data", {}).get("result", [])
            if not entry["result"]:
                entry["note"] = "empty series"
        except Exception as exc:
            entry["status"] = "error"
            entry["error"] = str(exc)
        results.append(entry)
        print(f"{entry['id']}: {entry['status']} ({len(entry.get('result', []))} series)")

    payload = {
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "thanos_host": host,
        "queries": results,
    }
    out = Path(args.output)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    print(f"Wrote {out}")


if __name__ == "__main__":
    main()

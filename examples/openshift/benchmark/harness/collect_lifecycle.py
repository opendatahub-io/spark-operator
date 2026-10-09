#!/usr/bin/env python3
"""Standalone lifecycle collection from existing SparkApplications on cluster.

Usage:
    python3 collect_lifecycle.py <run_id> [--results-dir ../results]

Discovers all spark-bench labelled apps on the cluster, collects lifecycle
timestamps, and writes JSON/CSV + summary.  Intended for re-collection after
a Locust run where the watcher timed out or was interrupted.
"""

from __future__ import annotations

import argparse
import logging
import sys
from pathlib import Path

from k8s_client import BENCH_LABEL, RUN_ID_LABEL, KubernetesClient
from lifecycle import ApplicationRecord, LifecycleWatcher, ResultsCollector
from export import export_results

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
)
logger = logging.getLogger("collect_lifecycle")

NAMESPACES = ["spark-bench-a", "spark-bench-b", "spark-bench-c"]


def main() -> None:
    parser = argparse.ArgumentParser(description="Collect lifecycle data from existing apps")
    parser.add_argument("run_id", help="Run ID to filter on (or 'all' for any spark-bench app)")
    parser.add_argument("--results-dir", default="../results", help="Base results directory")
    parser.add_argument("--namespaces", default=",".join(NAMESPACES), help="Comma-separated namespaces")
    parser.add_argument("--timeout", type=float, default=60.0, help="Seconds to poll (apps are already terminal)")
    args = parser.parse_args()

    namespaces = [ns.strip() for ns in args.namespaces.split(",") if ns.strip()]
    k8s = KubernetesClient()
    collector = ResultsCollector(run_id=args.run_id)

    # Discover existing apps on cluster
    total = 0
    for ns in namespaces:
        if args.run_id == "all":
            selector = f"{BENCH_LABEL}=true"
        else:
            selector = f"{BENCH_LABEL}=true,{RUN_ID_LABEL}={args.run_id}"
        apps = k8s.list_spark_applications(ns, label_selector=selector)
        for app in apps:
            name = app["metadata"]["name"]
            created = app["metadata"].get("creationTimestamp")
            collector.register(ns, name, submitted_at=created)
            total += 1
        logger.info("Found %d apps in %s", len(apps), ns)

    if total == 0:
        logger.error("No apps found. Check run_id and namespaces.")
        sys.exit(1)

    logger.info("Collecting lifecycle for %d apps", total)

    watcher = LifecycleWatcher(k8s, collector, poll_interval=3.0, timeout_seconds=args.timeout)
    records = watcher.wait_for_lifecycle()

    output_dir = Path(args.results_dir) / args.run_id
    paths = export_results(
        records,
        output_dir=output_dir,
        run_id=args.run_id,
        run_parameters={"source": "collect_lifecycle.py (re-collection)"},
    )
    logger.info("Wrote %d records to %s", len(records), {k: str(v) for k, v in paths.items()})

    # Quick summary
    states: dict[str, int] = {}
    for r in records:
        key = (r.terminal_state or "UNKNOWN").upper()
        states[key] = states.get(key, 0) + 1
    logger.info("States: %s", states)

    latencies = [r.start_latency_seconds for r in records if r.start_latency_seconds is not None]
    if latencies:
        logger.info(
            "Start latency: min=%.1fs, max=%.1fs, mean=%.1fs (%d of %d apps)",
            min(latencies), max(latencies), sum(latencies) / len(latencies),
            len(latencies), len(records),
        )


if __name__ == "__main__":
    main()

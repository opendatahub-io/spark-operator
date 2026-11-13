"""Export per-application records and aggregate summary to JSON/CSV."""

from __future__ import annotations

import csv
import json
import math
import statistics
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Optional

from lifecycle import ApplicationRecord


def _percentile(sorted_values: list[float], pct: float) -> Optional[float]:
    if not sorted_values:
        return None
    if len(sorted_values) == 1:
        return sorted_values[0]
    k = (len(sorted_values) - 1) * (pct / 100.0)
    f = math.floor(k)
    c = math.ceil(k)
    if f == c:
        return sorted_values[int(k)]
    return sorted_values[f] * (c - k) + sorted_values[c] * (k - f)


def build_summary(
    records: list[ApplicationRecord],
    run_id: str,
    run_parameters: Optional[dict[str, Any]] = None,
) -> dict[str, Any]:
    latencies = sorted(
        r.start_latency_seconds
        for r in records
        if r.start_latency_seconds is not None
    )
    states: dict[str, int] = {}
    for r in records:
        key = (r.terminal_state or "UNKNOWN").upper()
        states[key] = states.get(key, 0) + 1

    submitted_times = [
        r.submitted_at for r in records if r.submitted_at
    ]
    duration_min = None
    throughput = None
    if len(submitted_times) >= 2:
        from dateutil import parser as date_parser

        epochs = sorted(date_parser.isoparse(t).timestamp() for t in submitted_times)
        duration_sec = max(epochs) - min(epochs)
        # Include the last submission interval approximation (1/rate) is not known;
        # use wall clock from first to last submit; if zero, treat as 1s.
        duration_sec = max(duration_sec, 1.0)
        duration_min = duration_sec / 60.0
        throughput = round(len(records) / duration_min, 3)

    success = states.get("COMPLETED", 0)
    failed = sum(
        v
        for k, v in states.items()
        if k in {"FAILED", "SUBMISSION_FAILED", "FAILING"}
    )

    return {
        "run_id": run_id,
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "total_applications": len(records),
        "by_state": states,
        "success_count": success,
        "failed_count": failed,
        "accounted_count": len(records),
        "start_latency_seconds": {
            "count": len(latencies),
            "p50": _percentile(latencies, 50),
            "p90": _percentile(latencies, 90),
            "p99": _percentile(latencies, 99),
            "mean": round(statistics.mean(latencies), 3) if latencies else None,
            "min": latencies[0] if latencies else None,
            "max": latencies[-1] if latencies else None,
        },
        "submission_window_minutes": duration_min,
        "throughput_apps_per_min": throughput,
        "run_parameters": run_parameters or {},
    }


def export_results(
    records: list[ApplicationRecord],
    output_dir: Path,
    run_id: str,
    run_parameters: Optional[dict[str, Any]] = None,
) -> dict[str, Path]:
    output_dir.mkdir(parents=True, exist_ok=True)
    apps_json = output_dir / "applications.json"
    apps_csv = output_dir / "applications.csv"
    summary_json = output_dir / "summary.json"

    rows = [r.to_dict() for r in records]
    apps_json.write_text(json.dumps(rows, indent=2) + "\n", encoding="utf-8")

    fieldnames = list(ApplicationRecord.__dataclass_fields__.keys())
    with apps_csv.open("w", newline="", encoding="utf-8") as f:
        writer = csv.DictWriter(f, fieldnames=fieldnames)
        writer.writeheader()
        for row in rows:
            writer.writerow(row)

    summary = build_summary(records, run_id=run_id, run_parameters=run_parameters)
    summary_json.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")

    return {
        "applications_json": apps_json,
        "applications_csv": apps_csv,
        "summary_json": summary_json,
    }

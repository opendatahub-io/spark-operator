"""Lifecycle timestamp collection for submitted SparkApplications."""

from __future__ import annotations

import logging
import threading
import time
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from typing import Any, Optional

from dateutil import parser as date_parser

from k8s_client import KubernetesClient

TERMINAL_STATES = {
    "COMPLETED",
    "FAILED",
    "SUBMISSION_FAILED",
    "FAILING",
    "INVALIDATING",
}


def _parse_ts(value: Optional[str]) -> Optional[str]:
    if not value:
        return None
    try:
        dt = date_parser.isoparse(value)
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=timezone.utc)
        return dt.astimezone(timezone.utc).isoformat()
    except (ValueError, TypeError):
        return value


def _to_epoch(value: Optional[str]) -> Optional[float]:
    if not value:
        return None
    try:
        dt = date_parser.isoparse(value)
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=timezone.utc)
        return dt.timestamp()
    except (ValueError, TypeError):
        return None


def _pod_condition_time(pod: dict, condition_type: str) -> Optional[str]:
    for cond in pod.get("status", {}).get("conditions", []) or []:
        if cond.get("type") == condition_type and cond.get("status") == "True":
            return _parse_ts(cond.get("lastTransitionTime"))
    return None


@dataclass
class ApplicationRecord:
    name: str
    namespace: str
    run_id: str
    submitted_at: str
    created_at: Optional[str] = None
    driver_pod_name: Optional[str] = None
    driver_pod_created_at: Optional[str] = None
    driver_pod_scheduled_at: Optional[str] = None
    driver_pod_running_at: Optional[str] = None
    terminal_state: Optional[str] = None
    failure_reason: Optional[str] = None
    start_latency_seconds: Optional[float] = None
    completed_at: Optional[str] = None

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


@dataclass
class ResultsCollector:
    run_id: str
    records: dict[tuple[str, str], ApplicationRecord] = field(default_factory=dict)
    _lock: threading.Lock = field(default_factory=threading.Lock)

    def register(self, namespace: str, name: str, submitted_at: Optional[str] = None) -> None:
        key = (namespace, name)
        with self._lock:
            if key in self.records:
                return
            self.records[key] = ApplicationRecord(
                name=name,
                namespace=namespace,
                run_id=self.run_id,
                submitted_at=submitted_at or datetime.now(timezone.utc).isoformat(),
            )

    def list_records(self) -> list[ApplicationRecord]:
        with self._lock:
            return list(self.records.values())


class LifecycleWatcher:
    def __init__(
        self,
        k8s: KubernetesClient,
        collector: ResultsCollector,
        poll_interval: float = 5.0,
        timeout_seconds: float = 1800.0,
    ):
        self.k8s = k8s
        self.collector = collector
        self.poll_interval = poll_interval
        self.timeout_seconds = timeout_seconds
        self.logger = logging.getLogger("lifecycle")

    def refresh_record(self, record: ApplicationRecord) -> ApplicationRecord:
        try:
            app = self.k8s.get_spark_application(record.namespace, record.name)
        except Exception as exc:
            self.logger.debug(
                "get_spark_application failed for %s/%s: %s",
                record.namespace, record.name, exc,
            )
            app = None

        if app:
            record.created_at = _parse_ts(
                app.get("metadata", {}).get("creationTimestamp")
            )
            status = app.get("status") or {}
            app_state = status.get("applicationState") or {}
            state = app_state.get("state")
            if state:
                record.terminal_state = state
            err = app_state.get("errorMessage")
            if err:
                record.failure_reason = err
            term = status.get("terminationTime")
            if term:
                record.completed_at = _parse_ts(term)

        try:
            pod = self.k8s.find_driver_pod(record.namespace, record.name)
        except Exception as exc:
            self.logger.debug(
                "find_driver_pod failed for %s/%s: %s",
                record.namespace, record.name, exc,
            )
            pod = None

        if pod:
            record.driver_pod_name = pod.get("metadata", {}).get("name")
            record.driver_pod_created_at = _parse_ts(
                pod.get("metadata", {}).get("creationTimestamp")
            )
            record.driver_pod_scheduled_at = _pod_condition_time(pod, "PodScheduled")
            phase = pod.get("status", {}).get("phase")
            if phase == "Running" and not record.driver_pod_running_at:
                ready = _pod_condition_time(pod, "Ready")
                record.driver_pod_running_at = ready or _parse_ts(
                    datetime.now(timezone.utc).isoformat()
                )
            elif phase == "Running":
                pass
            elif phase in ("Failed", "Succeeded") and not record.driver_pod_running_at:
                record.driver_pod_running_at = record.driver_pod_scheduled_at or record.driver_pod_created_at

        created = _to_epoch(record.created_at) or _to_epoch(record.submitted_at)
        running = _to_epoch(record.driver_pod_running_at)
        if created is not None and running is not None:
            record.start_latency_seconds = round(running - created, 3)

        return record

    def wait_for_lifecycle(self) -> list[ApplicationRecord]:
        records = self.collector.list_records()
        if not records:
            self.logger.warning(
                "No applications registered; skipping lifecycle watch"
            )
            return []

        deadline = time.time() + self.timeout_seconds
        self.logger.info(
            "Watching lifecycle for %d applications (timeout=%ss)",
            len(records),
            self.timeout_seconds,
        )
        stall_count = 0
        prev_pending = len(records)
        while time.time() < deadline:
            records = self.collector.list_records()
            pending = 0
            stuck_names: list[str] = []
            for record in records:
                try:
                    self.refresh_record(record)
                except Exception as exc:
                    self.logger.warning(
                        "refresh_record error for %s/%s: %s",
                        record.namespace, record.name, exc,
                    )
                is_terminal = (record.terminal_state or "").upper() in TERMINAL_STATES
                if not is_terminal:
                    pending += 1
                    stuck_names.append(f"{record.namespace}/{record.name}(state={record.terminal_state})")
            self.logger.info(
                "Lifecycle progress: %d/%d in a terminal state",
                len(records) - pending,
                len(records),
            )
            if pending == 0:
                break
            if pending == prev_pending:
                stall_count += 1
            else:
                stall_count = 0
                prev_pending = pending
            if stall_count >= 10 and stuck_names:
                self.logger.warning(
                    "Stalled for %d iterations; stuck records (first 5): %s",
                    stall_count, stuck_names[:5],
                )
                stall_count = 0
            time.sleep(self.poll_interval)

        # Final refresh
        for record in self.collector.list_records():
            self.refresh_record(record)
        return self.collector.list_records()

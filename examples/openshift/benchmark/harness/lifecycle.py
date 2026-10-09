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

# Display order in progress/stall lines; unexpected states sort last.
STATE_DISPLAY_ORDER = [
    "PENDING",
    "SUBMITTED",
    "RUNNING",
    "SUCCEEDING",
    "PENDING_RERUN",
    "SUSPENDING",
    "SUSPENDED",
    "RESUMING",
    "COMPLETED",
    "FAILED",
    "SUBMISSION_FAILED",
    "INVALIDATING",
    "FAILING",
]

STALL_WARN_POLLS = 10
PROGRESS_BAR_WIDTH = 24


def _fmt_duration(seconds: float) -> str:
    seconds = max(0, int(seconds))
    if seconds < 60:
        return f"{seconds}s"
    minutes, sec = divmod(seconds, 60)
    if minutes < 60:
        return f"{minutes}m{sec:02d}s"
    hours, minutes = divmod(minutes, 60)
    return f"{hours}h{minutes:02d}m{sec:02d}s"


def _state_of(record: ApplicationRecord) -> str:
    """Current application state; empty (new) state is shown as PENDING."""
    return (record.terminal_state or "").strip().upper() or "PENDING"


def _is_terminal(record: ApplicationRecord) -> bool:
    return _state_of(record) in TERMINAL_STATES


def _state_counts(records: list[ApplicationRecord]) -> dict[str, int]:
    counts: dict[str, int] = {}
    for record in records:
        state = _state_of(record)
        counts[state] = counts.get(state, 0) + 1
    return counts


def _fmt_state_counts(counts: dict[str, int]) -> str:
    ordered = [s for s in STATE_DISPLAY_ORDER if s in counts]
    ordered += sorted(s for s in counts if s not in STATE_DISPLAY_ORDER)
    return " ".join(f"{state}={counts[state]}" for state in ordered)


def _progress_bar(done: int, total: int, width: int = PROGRESS_BAR_WIDTH) -> str:
    if total <= 0:
        return "[" + "-" * width + "]"
    filled = round(width * done / total)
    return "[" + "#" * filled + "-" * (width - filled) + "]"


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

    def _log_intro(self, total: int) -> None:
        self.logger.info(
            "Waiting for %d SparkApplications to reach a terminal state (%s).",
            total,
            "/".join(sorted(TERMINAL_STATES)),
        )
        self.logger.info(
            "This is the post-run measurement phase: for each app we record CR "
            "created -> driver pod Running (start latency) -> terminal state. "
            "SUBMITTED/RUNNING/SUCCEEDING are normal in-flight states while the "
            "operator schedules drivers and Spark jobs run."
        )
        self.logger.info(
            "Polling every %.0fs; each poll reads every app + driver pod from the "
            "API (2 calls per app), so a poll cycle itself takes several seconds "
            "at this app count. Timeout: %s.",
            self.poll_interval,
            _fmt_duration(self.timeout_seconds),
        )

    def _log_stall(
        self,
        records: list[ApplicationRecord],
        state_since: dict[tuple[str, str], float],
        now: float,
        stalled_for: float,
    ) -> None:
        stuck = [r for r in records if not _is_terminal(r)]
        if not stuck:
            return
        self.logger.warning(
            "No new completions for %s: %d app(s) still in flight (%s)",
            _fmt_duration(stalled_for),
            len(stuck),
            _fmt_state_counts(_state_counts(stuck)),
        )
        for record in stuck[:5]:
            dwell = now - state_since.get((record.namespace, record.name), now)
            if record.driver_pod_name:
                pod_desc = (
                    record.driver_pod_name
                    if record.driver_pod_running_at
                    else f"{record.driver_pod_name} (not Running yet)"
                )
            else:
                pod_desc = "none created yet"
            self.logger.warning(
                "  stuck: %s/%s in %s for %s | driver pod: %s",
                record.namespace,
                record.name,
                _state_of(record),
                _fmt_duration(dwell),
                pod_desc,
            )
        if len(stuck) > 5:
            self.logger.warning("  ... and %d more", len(stuck) - 5)
        self.logger.warning(
            "To dig in: oc describe sparkapplication <name> -n <namespace> | "
            "oc get pods -n <namespace> | oc logs <driver-pod> -n <namespace>"
        )

    def wait_for_lifecycle(self) -> list[ApplicationRecord]:
        records = self.collector.list_records()
        if not records:
            self.logger.warning(
                "No applications registered; skipping lifecycle watch"
            )
            return []

        started = time.time()
        deadline = started + self.timeout_seconds
        self._log_intro(len(records))

        # (namespace, name) -> state at last poll, and when that state was first seen.
        prev_states: dict[tuple[str, str], str] = {}
        state_since: dict[tuple[str, str], float] = {}
        last_terminal: Optional[int] = None
        last_progress_at = started
        stall_polls = 0

        while time.time() < deadline:
            poll_started = time.time()
            records = self.collector.list_records()
            for record in records:
                try:
                    self.refresh_record(record)
                except Exception as exc:
                    self.logger.warning(
                        "refresh_record error for %s/%s: %s",
                        record.namespace, record.name, exc,
                    )

            poll_now = time.time()
            transitions: list[tuple[ApplicationRecord, str, str, float]] = []
            for record in records:
                key = (record.namespace, record.name)
                state = _state_of(record)
                prev = prev_states.get(key)
                if prev is None:
                    # Baseline pass: seed states without logging transitions.
                    prev_states[key] = state
                    state_since[key] = poll_now
                    continue
                if state != prev:
                    dwell = poll_now - state_since.get(key, poll_now)
                    transitions.append((record, prev, state, dwell))
                    prev_states[key] = state
                    state_since[key] = poll_now

            for record, old_state, new_state, dwell in transitions:
                self.logger.info(
                    "  %s/%s: %s -> %s (after %s)",
                    record.namespace, record.name, old_state, new_state,
                    _fmt_duration(dwell),
                )

            total = len(records)
            terminal = sum(1 for r in records if _is_terminal(r))
            pct = round(100 * terminal / total) if total else 100
            self.logger.info(
                "progress %s %d/%d apps done (%d%%) | elapsed %s | poll %.1fs | %s",
                _progress_bar(terminal, total),
                terminal,
                total,
                pct,
                _fmt_duration(poll_now - started),
                poll_now - poll_started,
                _fmt_state_counts(_state_counts(records)),
            )

            if total and terminal >= total:
                break

            if last_terminal is not None and terminal == last_terminal:
                stall_polls += 1
            else:
                stall_polls = 0
                last_progress_at = poll_now
            last_terminal = terminal

            if stall_polls >= STALL_WARN_POLLS:
                self._log_stall(records, state_since, poll_now,
                                poll_now - last_progress_at)
                stall_polls = 0

            time.sleep(self.poll_interval)
        else:
            self.logger.warning(
                "Timed out after %s; doing a final refresh and exporting what "
                "was collected so far",
                _fmt_duration(self.timeout_seconds),
            )

        # Final refresh
        for record in self.collector.list_records():
            self.refresh_record(record)
        records = self.collector.list_records()

        terminal = sum(1 for r in records if _is_terminal(r))
        self.logger.info(
            "Lifecycle watch complete: %d/%d apps reached a terminal state in %s | %s",
            terminal,
            len(records),
            _fmt_duration(time.time() - started),
            _fmt_state_counts(_state_counts(records)),
        )
        stuck = [r for r in records if not _is_terminal(r)]
        for record in stuck[:10]:
            self.logger.warning(
                "  never finished: %s/%s state=%s",
                record.namespace, record.name, _state_of(record),
            )
        if len(stuck) > 10:
            self.logger.warning("  ... and %d more", len(stuck) - 10)
        return records

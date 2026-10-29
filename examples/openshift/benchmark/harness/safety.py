"""Safety stop: etcd alarm or sustained create failures (RHAIENG-7484 / RHAI-962).

Numeric extra windows are an Open Question on RHAISTRAT-2591. This module only
implements the two required stops:

1. etcd alarm / no leader (queried via Thanos when reachable)
2. Locust --jobs-max-failures (wired in locustfile; documented here)
"""

from __future__ import annotations

import json
import logging
import os
import ssl
import subprocess
import threading
import time
import urllib.parse
import urllib.request
from typing import Any, Callable, Optional

logger = logging.getLogger("spark_bench_safety")


def _env_truthy(name: str, default: str = "false") -> bool:
    return os.environ.get(name, default).strip().lower() in {"1", "true", "yes", "y"}


def _oc(*args: str) -> str:
    result = subprocess.run(
        ["oc", *args],
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise RuntimeError((result.stderr or result.stdout or "").strip())
    return result.stdout.strip()


def _tls_context() -> ssl.SSLContext:
    if _env_truthy("SPARK_BENCH_INSECURE_SKIP_TLS_VERIFY", "true"):
        context = ssl.create_default_context()
        context.check_hostname = False  # tls-lint:ignore
        context.verify_mode = ssl.CERT_NONE  # tls-lint:ignore
        return context
    ca_bundle = _oc(
        "get",
        "configmap",
        "default-ingress-cert",
        "-n",
        "openshift-config-managed",
        "-o",
        "jsonpath={.data.ca-bundle\\.crt}",
    )
    return ssl.create_default_context(cadata=ca_bundle)


def _thanos_query(expr: str) -> list[dict[str, Any]]:
    host = _oc(
        "get",
        "route",
        "thanos-querier",
        "-n",
        "openshift-monitoring",
        "-o",
        "jsonpath={.spec.host}",
    )
    token = _oc("whoami", "-t")
    url = f"https://{host}/api/v1/query?{urllib.parse.urlencode({'query': expr})}"
    req = urllib.request.Request(
        url, headers={"Authorization": f"Bearer {token}"}
    )
    with urllib.request.urlopen(req, context=_tls_context(), timeout=20) as resp:
        body = json.loads(resp.read().decode())
    if body.get("status") != "success":
        raise RuntimeError(body.get("error") or "thanos query failed")
    return body.get("data", {}).get("result") or []


def etcd_alarm_active() -> tuple[bool, str]:
    """Return (should_stop, reason).

    Stops when any etcd instance reports no leader, or when a firing ALERTS
    series matches etcd. Query failures do not stop the run (access may be
    missing); they are logged.
    """
    try:
        leaders = _thanos_query("etcd_server_has_leader")
        if leaders and all(float(s.get("value", [0, "1"])[1]) < 1 for s in leaders):
            return True, "all etcd_server_has_leader samples are 0"
        if leaders:
            for series in leaders:
                if float(series.get("value", [0, "1"])[1]) < 1:
                    return True, "etcd_server_has_leader=0 for at least one instance"
    except Exception as e:
        logger.warning("etcd leader check skipped: %s", e)

    try:
        alerts = _thanos_query(
            'ALERTS{alertstate="firing",alertname=~".*[Ee]tcd.*"}'
        )
        if alerts:
            names = sorted(
                {
                    s.get("metric", {}).get("alertname", "etcd")
                    for s in alerts
                }
            )
            return True, f"firing etcd alerts: {', '.join(names)}"
    except Exception as e:
        logger.warning("etcd ALERTS check skipped: %s", e)

    return False, ""


class SafetyMonitor:
    """Background poller that calls stop_fn when an etcd alarm is seen."""

    def __init__(
        self,
        stop_fn: Callable[[], None],
        interval_seconds: float = 30.0,
        enabled: bool = True,
    ):
        self.stop_fn = stop_fn
        self.interval_seconds = interval_seconds
        self.enabled = enabled
        self._thread: Optional[threading.Thread] = None
        self._stop = threading.Event()
        self.triggered_reason: Optional[str] = None

    def start(self) -> None:
        if not self.enabled:
            logger.info("Safety monitor disabled (SPARK_BENCH_SAFETY_STOP=false)")
            return
        self._thread = threading.Thread(target=self._loop, name="safety-monitor", daemon=True)
        self._thread.start()
        logger.info(
            "Safety monitor started (interval=%ss). "
            "Sustained create failures use Locust --jobs-max-failures.",
            self.interval_seconds,
        )

    def stop(self) -> None:
        self._stop.set()
        if self._thread and self._thread.is_alive():
            self._thread.join(timeout=5)

    def _loop(self) -> None:
        while not self._stop.wait(self.interval_seconds):
            try:
                active, reason = etcd_alarm_active()
            except Exception as e:
                logger.warning("safety poll error: %s", e)
                continue
            if active:
                self.triggered_reason = reason
                logger.error("SAFETY STOP: %s", reason)
                try:
                    self.stop_fn()
                except Exception as e:
                    logger.error("stop_fn failed: %s", e)
                return

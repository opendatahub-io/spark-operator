"""Locust load generator for OpenShift AI Spark Operator benchmarks.

Adapted from awslabs/data-on-eks spark-operator-benchmarks with OpenShift-specific
additions: run-id labels, configurable service account, lifecycle timestamps,
and JSON/CSV export.
"""

from __future__ import annotations

import copy
import logging
import os
import random
import re
import string
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, Optional

import yaml
from locust import HttpUser, constant, env, events, task

from export import export_results
from k8s_client import BENCH_LABEL, RUN_ID_LABEL, KubernetesClient
from lifecycle import LifecycleWatcher, ResultsCollector

# Shared across Locust workers in a single process (headless local run).
_RUN_ID: Optional[str] = None
_COLLECTOR: Optional[ResultsCollector] = None
_OUTPUT_DIR: Optional[Path] = None


def _get_run_id(parsed) -> str:
    global _RUN_ID
    if _RUN_ID:
        return _RUN_ID
    _RUN_ID = parsed.run_id or datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    return _RUN_ID


def _get_collector(parsed) -> ResultsCollector:
    global _COLLECTOR
    if _COLLECTOR is None:
        _COLLECTOR = ResultsCollector(run_id=_get_run_id(parsed))
    return _COLLECTOR


def _get_output_dir(parsed) -> Path:
    global _OUTPUT_DIR
    if _OUTPUT_DIR is None:
        base = Path(parsed.results_dir)
        _OUTPUT_DIR = base / _get_run_id(parsed)
        _OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    return _OUTPUT_DIR


class Configuration:
    def __init__(self, environment: env.Environment):
        parsed = environment.parsed_options
        self.template_path = parsed.spark_template
        self.name_prefix = parsed.spark_name_prefix
        self.name_suffix_length = parsed.spark_name_length
        self.max_jobs = parsed.job_limit_per_user
        self.max_failures = parsed.jobs_max_failures
        self.submission_rate = parsed.jobs_per_min
        self.namespaces = [ns.strip() for ns in parsed.spark_namespaces.split(",") if ns.strip()]
        self.cleanup_apps = not parsed.no_spark_cleanup
        self.service_account = parsed.spark_service_account
        self.run_id = _get_run_id(parsed)
        self.results_dir = parsed.results_dir
        self.lifecycle_timeout = parsed.lifecycle_timeout
        self.validate()

    def validate(self) -> None:
        if not os.path.exists(self.template_path):
            raise FileNotFoundError(f"Template file not found: {self.template_path}")
        if not re.match(r"^[a-z][-a-z0-9]*$", self.name_prefix):
            raise ValueError("Invalid name_prefix format")
        if self.name_suffix_length < 1:
            raise ValueError("name_suffix_length must be positive")
        if self.max_jobs < 1:
            raise ValueError("job_limit_per_user must be positive")
        if self.max_failures < 0:
            raise ValueError("max_failures must be non-negative")
        if self.submission_rate <= 0:
            raise ValueError("submission_rate must be positive")
        if not self.namespaces:
            raise ValueError("namespaces list cannot be empty")
        for ns in self.namespaces:
            if not re.match(r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$", ns):
                raise ValueError(f"Invalid namespace format: {ns}")


@events.init_command_line_parser.add_listener
def on_parser_init(parser):
    parser.add_argument(
        "--spark-template",
        help="Path to SparkApplication template",
        env_var="LOAD_TEST_TEMPLATE_PATH",
        default="../workloads/spark-app-openshift.yaml",
    )
    parser.add_argument(
        "--spark-name-prefix",
        help="Prefix for generated names",
        env_var="LOAD_TEST_NAME_PREFIX",
        default="spark-bench",
    )
    parser.add_argument(
        "--spark-name-length",
        type=int,
        help="Length of random name suffix",
        env_var="LOAD_TEST_NAME_SUFFIX_LENGTH",
        default=8,
    )
    parser.add_argument(
        "--job-limit-per-user",
        type=int,
        help="Maximum number of applications to submit per user",
        env_var="LOAD_TEST_JOB_SIZE",
        default=17,
    )
    parser.add_argument(
        "--jobs-max-failures",
        type=int,
        help="Maximum number of failures before stopping",
        env_var="LOAD_TEST_MAX_FAILURES",
        default=5,
    )
    parser.add_argument(
        "--jobs-per-min",
        type=float,
        help="Submissions per minute (per user wait interval uses this rate)",
        env_var="LOAD_TEST_SUBMISSION_RATE",
        default=30.0,
    )
    parser.add_argument(
        "--spark-namespaces",
        help="Comma-separated list of namespaces",
        env_var="LOAD_TEST_NAMESPACES",
        default="spark-bench-a,spark-bench-b,spark-bench-c",
    )
    parser.add_argument(
        "--spark-service-account",
        help="Driver service account name (must exist in each namespace)",
        env_var="LOAD_TEST_SERVICE_ACCOUNT",
        default="spark",
    )
    parser.add_argument(
        "--no-spark-cleanup",
        action="store_true",
        help="If set, Spark applications will not be deleted after test",
        env_var="LOAD_TEST_NO_CLEANUP",
        default=False,
    )
    parser.add_argument(
        "--run-id",
        help="Run identifier used for labels and results directory",
        env_var="LOAD_TEST_RUN_ID",
        default="",
    )
    parser.add_argument(
        "--results-dir",
        help="Base directory for JSON/CSV output",
        env_var="LOAD_TEST_RESULTS_DIR",
        default="../results",
    )
    parser.add_argument(
        "--lifecycle-timeout",
        type=float,
        help="Seconds to wait for driver Running / terminal state after submissions",
        env_var="LOAD_TEST_LIFECYCLE_TIMEOUT",
        default=1800.0,
    )


def generate_spark_name(prefix: str = "spark-bench", length: int = 8) -> str:
    if length < 1:
        raise ValueError("Length must be positive")
    if not prefix or not re.match(r"^[a-z][-a-z0-9]*$", prefix):
        raise ValueError(
            "Prefix must start with lowercase letter and contain only "
            "lowercase letters, numbers, and hyphens"
        )
    chars = string.ascii_lowercase + string.digits
    suffix = "".join(random.choice(chars) for _ in range(length))
    return f"{prefix}-{suffix}"


class TemplateManager:
    def __init__(self, template_path: str, service_account: str):
        self.template_path = template_path
        self.service_account = service_account
        self.template_content = None
        self.load_template()

    def load_template(self) -> None:
        with open(self.template_path, "r", encoding="utf-8") as f:
            self.template_content = yaml.safe_load(f)
        if not isinstance(self.template_content, dict):
            raise ValueError("Template must be a valid YAML mapping")

    def substitute_variables(self, variables: Dict[str, Any]) -> dict:
        template = copy.deepcopy(self.template_content)
        template["metadata"]["name"] = variables["name"]
        template["metadata"]["namespace"] = variables["namespace"]
        labels = template["metadata"].setdefault("labels", {})
        labels[BENCH_LABEL] = "true"
        labels[RUN_ID_LABEL] = variables["run_id"]

        spark_conf = template["spec"].setdefault("sparkConf", {})
        spark_conf["spark.kubernetes.executor.podNamePrefix"] = variables["name"]

        template["spec"]["driver"]["serviceAccount"] = self.service_account
        return template


class SparkLoadTest(HttpUser):
    host = "http://localhost"

    def wait_time(self):
        return constant(60 / self.config.submission_rate)(self)

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.logger = logging.getLogger("spark_load_test")
        self.logger.setLevel(logging.INFO)
        if not self.logger.handlers:
            handler = logging.StreamHandler()
            handler.setFormatter(
                logging.Formatter("%(asctime)s - %(name)s - %(levelname)s - %(message)s")
            )
            self.logger.addHandler(handler)

        self.config = Configuration(self.environment)
        self.template_manager = TemplateManager(
            self.config.template_path, self.config.service_account
        )
        self.failure_count = 0
        self.namespace_index = 0
        self.application_count = 0
        self.k8s_client = KubernetesClient()
        self.collector = _get_collector(self.environment.parsed_options)
        _get_output_dir(self.environment.parsed_options)
        self.logger.info(
            "Run id=%s namespaces=%s sa=%s jobs_per_user=%s rate=%s/min",
            self.config.run_id,
            self.config.namespaces,
            self.config.service_account,
            self.config.max_jobs,
            self.config.submission_rate,
        )

    def on_start(self):
        try:
            for namespace in self.config.namespaces:
                if not self.k8s_client.namespace_exists(namespace):
                    raise ValueError(
                        f"Namespace {namespace} does not exist. Run setup/setup.sh first."
                    )
        except Exception:
            self.environment.runner.quit()
            raise

    @task(1)
    def submit_spark_applications(self):
        if self.failure_count >= self.config.max_failures:
            self.logger.error(
                "Failure threshold reached (%s failures)", self.failure_count
            )
            self.environment.runner.quit()
            return

        if self.application_count >= self.config.max_jobs:
            self.logger.info("Maximum job count reached for this user")
            self.stop()
            # Headless runs with a long --run-time otherwise idle until timeout.
            runner = getattr(self.environment, "runner", None)
            if runner is not None and getattr(runner, "user_count", 1) <= 1:
                self.logger.info("All users finished submissions; quitting Locust")
                runner.quit()
            return

        submission_start_time = time.time()
        try:
            namespace = self.config.namespaces[
                self.namespace_index % len(self.config.namespaces)
            ]
            name = generate_spark_name(
                prefix=self.config.name_prefix,
                length=self.config.name_suffix_length,
            )
            body = self.template_manager.substitute_variables(
                {
                    "name": name,
                    "namespace": namespace,
                    "run_id": self.config.run_id,
                }
            )
            labels = body["metadata"].get("labels", {})
            self.logger.info(
                "Submitting SparkApplication %s to namespace %s", name, namespace
            )
            created = self.k8s_client.create_spark_application(
                namespace, name, body["spec"], labels=labels
            )
            created_at = created.get("metadata", {}).get("creationTimestamp")
            self.collector.register(
                namespace,
                name,
                submitted_at=created_at
                or datetime.now(timezone.utc).isoformat(),
            )
            self.application_count += 1
            self.namespace_index += 1

            self.environment.events.request.fire(
                request_type="SparkApplication",
                name="application_created",
                response_time=(time.time() - submission_start_time) * 1000,
                response_length=0,
                exception=None,
            )
        except Exception as e:
            self.failure_count += 1
            self.logger.error("Failed to submit SparkApplication: %s", e)
            self.environment.events.request.fire(
                request_type="SparkApplication",
                name="application_created",
                response_time=(time.time() - submission_start_time) * 1000,
                response_length=0,
                exception=e,
            )


@events.quitting.add_listener
def on_quitting(environment: env.Environment, **kwargs):
    logger = logging.getLogger("post_run")
    parsed = environment.parsed_options
    run_id = _get_run_id(parsed)
    collector = _get_collector(parsed)
    output_dir = _get_output_dir(parsed)
    k8s = KubernetesClient()

    logger.info("Collecting lifecycle timestamps for run %s", run_id)
    watcher = LifecycleWatcher(
        k8s,
        collector,
        timeout_seconds=parsed.lifecycle_timeout,
    )
    records = watcher.wait_for_lifecycle()

    run_parameters = {
        "namespaces": parsed.spark_namespaces.split(","),
        "job_limit_per_user": parsed.job_limit_per_user,
        "jobs_per_min": parsed.jobs_per_min,
        "spark_template": parsed.spark_template,
        "spark_service_account": parsed.spark_service_account,
        "users": getattr(parsed, "num_users", None),
    }
    paths = export_results(
        records, output_dir=output_dir, run_id=run_id, run_parameters=run_parameters
    )
    logger.info("Wrote results to %s", paths)

    if parsed.no_spark_cleanup:
        logger.info("Skipping cleanup (--no-spark-cleanup)")
        return

    selector = f"{RUN_ID_LABEL}={run_id}"
    for namespace in parsed.spark_namespaces.split(","):
        namespace = namespace.strip()
        if not namespace:
            continue
        try:
            k8s.delete_spark_applications_by_label(namespace, selector)
        except Exception as e:
            logger.error("Cleanup failed in %s: %s", namespace, e)

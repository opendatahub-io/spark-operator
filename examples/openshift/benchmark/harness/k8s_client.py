"""Kubernetes client for SparkApplication CRUD and pod lifecycle lookups."""

from __future__ import annotations

import logging
import os
from typing import Any, Optional

from kubernetes import client, config
from kubernetes.client.rest import ApiException
from urllib3.exceptions import MaxRetryError, SSLError as Urllib3SSLError

SPARK_GROUP = "sparkoperator.k8s.io"
SPARK_VERSION = "v1beta2"
SPARK_PLURAL = "sparkapplications"

BENCH_LABEL = "spark-bench"
RUN_ID_LABEL = "spark-bench.run-id"


def _env_truthy(name: str, default: str = "false") -> bool:
    return os.environ.get(name, default).strip().lower() in {"1", "true", "yes", "y"}


class KubernetesClient:
    def __init__(self, context: Optional[str] = None):
        self.logger = logging.getLogger("k8s_client")
        self.logger.setLevel(logging.INFO)
        self._initialize_client(context)
        self.custom_api = client.CustomObjectsApi()
        self.core_api = client.CoreV1Api()

    def _initialize_client(self, context: Optional[str]) -> None:
        try:
            if context:
                config.load_kube_config(context=context)
            else:
                try:
                    config.load_incluster_config()
                except config.ConfigException:
                    config.load_kube_config()

            # macOS/Python often fails ROSA API TLS verify even when `oc` works.
            # Set SPARK_BENCH_INSECURE_SKIP_TLS_VERIFY=true (run.sh default) to skip.
            if _env_truthy("SPARK_BENCH_INSECURE_SKIP_TLS_VERIFY", "false"):
                cfg = client.Configuration.get_default_copy()
                cfg.verify_ssl = False
                cfg.assert_hostname = False
                client.Configuration.set_default(cfg)
                # urllib3 v2 warns loudly; keep logs readable for load runs.
                try:
                    import urllib3

                    urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
                except Exception:
                    pass
                self.logger.warning(
                    "TLS certificate verification disabled "
                    "(SPARK_BENCH_INSECURE_SKIP_TLS_VERIFY=true)"
                )
        except Exception as e:
            raise RuntimeError(f"Could not initialize Kubernetes client: {e}") from e

    def namespace_exists(self, namespace: str) -> bool:
        try:
            self.core_api.read_namespace(namespace)
            return True
        except ApiException as e:
            if e.status == 404:
                return False
            raise RuntimeError(f"Failed to check namespace {namespace}: {e}") from e
        except (Urllib3SSLError, MaxRetryError, Exception) as e:
            msg = str(e)
            if "CERTIFICATE_VERIFY_FAILED" in msg or "SSL" in msg:
                raise RuntimeError(
                    f"TLS verify failed talking to the API for namespace {namespace}. "
                    "Re-run with SPARK_BENCH_INSECURE_SKIP_TLS_VERIFY=true "
                    f"(or fix the Python trust store). Underlying error: {e}"
                ) from e
            raise

    def create_spark_application(
        self,
        namespace: str,
        name: str,
        spec: dict,
        labels: Optional[dict[str, str]] = None,
    ) -> dict:
        metadata: dict[str, Any] = {"name": name, "namespace": namespace}
        if labels:
            metadata["labels"] = labels
        body = {
            "apiVersion": f"{SPARK_GROUP}/{SPARK_VERSION}",
            "kind": "SparkApplication",
            "metadata": metadata,
            "spec": spec,
        }
        try:
            return self.custom_api.create_namespaced_custom_object(
                group=SPARK_GROUP,
                version=SPARK_VERSION,
                namespace=namespace,
                plural=SPARK_PLURAL,
                body=body,
            )
        except Exception as e:
            self.logger.error("Failed to create SparkApplication %s: %s", name, e)
            raise RuntimeError(f"Failed to create SparkApplication: {e}") from e

    def get_spark_application(self, namespace: str, name: str) -> Optional[dict]:
        try:
            return self.custom_api.get_namespaced_custom_object(
                group=SPARK_GROUP,
                version=SPARK_VERSION,
                namespace=namespace,
                plural=SPARK_PLURAL,
                name=name,
            )
        except ApiException as e:
            if e.status == 404:
                return None
            raise RuntimeError(f"Failed to get SparkApplication: {e}") from e

    def list_spark_applications(
        self, namespace: str, label_selector: Optional[str] = None
    ) -> list:
        response = self.custom_api.list_namespaced_custom_object(
            group=SPARK_GROUP,
            version=SPARK_VERSION,
            namespace=namespace,
            plural=SPARK_PLURAL,
            label_selector=label_selector,
        )
        return response.get("items", [])

    def delete_spark_applications_by_label(
        self, namespace: str, label_selector: str
    ) -> None:
        self.logger.info(
            "Deleting SparkApplications in %s matching %s", namespace, label_selector
        )
        self.custom_api.delete_collection_namespaced_custom_object(
            group=SPARK_GROUP,
            version=SPARK_VERSION,
            namespace=namespace,
            plural=SPARK_PLURAL,
            label_selector=label_selector,
        )

    def find_driver_pod(self, namespace: str, app_name: str) -> Optional[dict]:
        """Find the driver pod for a SparkApplication.

        Spark Operator labels driver pods with spark-role=driver and
        sparkoperator.k8s.io/app-name=<app>.
        """
        selectors = [
            f"spark-role=driver,sparkoperator.k8s.io/app-name={app_name}",
            f"spark-role=driver,spark-app-name={app_name}",
        ]
        for selector in selectors:
            try:
                pods = self.core_api.list_namespaced_pod(
                    namespace=namespace, label_selector=selector
                )
                if pods.items:
                    return self.core_api.api_client.sanitize_for_serialization(
                        pods.items[0]
                    )
            except ApiException as e:
                self.logger.debug("Pod list failed for %s: %s", selector, e)
        # Fallback: name prefix used by Spark on Kubernetes
        try:
            pods = self.core_api.list_namespaced_pod(namespace=namespace)
            for pod in pods.items:
                if pod.metadata.name.startswith(f"{app_name}-driver"):
                    return self.core_api.api_client.sanitize_for_serialization(pod)
        except ApiException as e:
            self.logger.debug("Fallback pod list failed: %s", e)
        return None

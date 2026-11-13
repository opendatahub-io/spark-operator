import json
import sys
from pathlib import Path
from tempfile import TemporaryDirectory
from unittest import TestCase
from unittest.mock import patch

import capacity_snapshot
from capacity_snapshot import _qualify_capacity, _workload_requirements, collect


class CapacitySnapshotTest(TestCase):
    def test_checked_in_template_derives_demand_without_hardcoded_constants(self):
        template = Path(__file__).parents[1] / "workloads" / "spark-app-openshift.yaml"

        required = _workload_requirements(template, 51)

        self.assertEqual(required["image"], "docker.io/apache/spark:4.0.1")
        self.assertEqual(required["image_pull_policy"], "IfNotPresent")
        self.assertEqual(required["cpu_per_application_m"], 600)
        self.assertEqual(required["pods_per_application"], 6)
        self.assertEqual(required["required_cpu_m"], 30_600)
        self.assertEqual(required["required_pods"], 306)

    def test_qualification_requires_both_cpu_and_pod_headroom(self):
        required = {"required_cpu_m": 30_600, "required_pods": 306}

        self.assertTrue(_qualify_capacity(15_300, 153, required)["passed"])
        self.assertFalse(_qualify_capacity(1_595, 306, required)["passed"])
        self.assertFalse(_qualify_capacity(30_600, 152, required)["passed"])

    def test_collect_subtracts_existing_requests_from_schedulable_headroom(self):
        node = {
            "metadata": {"name": "worker-1", "labels": {}},
            "spec": {},
            "status": {
                "allocatable": {"cpu": "4", "memory": "8Gi", "pods": "10"},
                "conditions": [{"type": "Ready", "status": "True"}],
            },
        }
        pod = {
            "spec": {
                "nodeName": "worker-1",
                "containers": [
                    {"resources": {"requests": {"cpu": "500m"}}},
                ],
            },
            "status": {"phase": "Running"},
        }

        def fake_oc_json(*args):
            if args == ("get", "nodes"):
                return {"items": [node]}
            if args == ("get", "pods", "--all-namespaces"):
                return {"items": [pod]}
            return {"items": []}

        with patch.object(capacity_snapshot, "_oc_json", side_effect=fake_oc_json):
            snapshot = collect([])

        self.assertEqual(snapshot["totals"]["available_cpu_m"], 3_500)
        self.assertEqual(snapshot["totals"]["available_pods"], 9)

    def test_ack_low_capacity_is_the_only_override(self):
        payload = {
            "totals": {
                "allocatable_cpu_m": 16_000,
                "allocatable_memory_mi": 1,
                "allocatable_pods": 250,
                "available_cpu_m": 1_595,
                "available_pods": 200,
            },
            "workload": {
                "required_cpu_m": 30_600,
                "required_pods": 306,
            },
            "qualification": {
                "minimum_capacity_ratio": 0.5,
                "available_to_required_ratio": {
                    "cpu": 1_595 / 30_600,
                    "pods": 200 / 306,
                },
                "passed": False,
                "checks": {"cpu": False, "pods": True},
            },
        }
        with TemporaryDirectory() as directory:
            output = Path(directory) / "capacity.json"
            base_argv = [
                "capacity_snapshot.py",
                str(output),
                "spark-bench-a",
                "--template",
                "workload.yaml",
                "--applications",
                "51",
            ]
            with (
                patch.object(capacity_snapshot, "collect", return_value=payload),
                patch.object(sys, "argv", base_argv),
            ):
                self.assertEqual(capacity_snapshot.main(), 2)
            with (
                patch.object(capacity_snapshot, "collect", return_value=payload),
                patch.object(sys, "argv", [*base_argv, "--ack-low-capacity"]),
            ):
                self.assertEqual(capacity_snapshot.main(), 0)

            saved = json.loads(output.read_text(encoding="utf-8"))
            self.assertTrue(saved["qualification"]["low_capacity_acknowledged"])

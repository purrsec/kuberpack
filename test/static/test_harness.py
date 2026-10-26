"""Regression tests for false-green results, deadlines and secret redaction."""
import base64
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import xml.etree.ElementTree as ET
import yaml

COMMON = Path(__file__).resolve().parents[1] / "common"
sys.path.insert(0, str(COMMON))
from suite import Suite
from privacy import collect_values, redact


class HarnessContracts(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.environment = patch.dict(os.environ, {
            "KUBERPACK_TEST_ARTIFACTS": self.directory.name,
            "KUBERPACK_TEST_RUN": "kpt-unit-test",
            "KUBERPACK_TEST_UID": str(os.getuid()),
            "KUBERPACK_TEST_GID": str(os.getgid()),
        })
        self.environment.start()
        self.addCleanup(self.environment.stop)

    def test_nonzero_exit_remains_a_failure_in_both_reports(self):
        suite = Suite("unit-light")
        suite.secret_values.add("private-token-example")
        with self.assertRaises(RuntimeError):
            suite.command("failing fixture", [sys.executable, "-c", "print('private-token-example'); raise SystemExit(7)"])
        suite.report()
        report = json.loads((Path(self.directory.name) / "result.json").read_text())
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["checks"][0]["actual"], 7)
        self.assertEqual(ET.parse(Path(self.directory.name) / "junit.xml").getroot().get("failures"), "1")
        for path in Path(self.directory.name).iterdir():
            self.assertNotIn("private-token-example", path.read_text())

    def test_timeout_stops_the_process_instead_of_reporting_success(self):
        suite = Suite("unit-light")
        with self.assertRaises(RuntimeError):
            suite.command("deadline", [sys.executable, "-c", "import time; time.sleep(30)"], timeout=0.1)
        self.assertEqual(suite.checks[0]["actual"], 124)
        self.assertLess(suite.checks[0]["duration"], 3)

    def test_diagnostics_redact_raw_and_encoded_kubernetes_credentials(self):
        token = "fixture-private-token-0987654321"
        encoded = base64.b64encode(token.encode()).decode()
        public_url = "https://infisical.fixture.test"
        response = {"items": [{"metadata": {"name": "service-identity"}, "data": {
            "clientSecret": encoded,
            "url": base64.b64encode(public_url.encode()).decode(),
        }}]}
        with patch("privacy.Path.exists", side_effect=[False, True]), patch("privacy.subprocess.run", return_value=
                   subprocess.CompletedProcess([], 0, json.dumps(response))):
            values, complete = collect_values()
        self.assertTrue(complete)
        basic = base64.b64encode(("kuberpack:" + token).encode()).decode()
        self.assertEqual(redact(" ".join((token, encoded, basic)), values), "[redacted] [redacted] [redacted]")
        self.assertEqual(redact(public_url, values), public_url)

    def test_privacy_failure_is_reported_after_sanitizing_the_artifact(self):
        import check_privacy
        path = Path(self.directory.name) / "leaked.log"
        path.write_text("before fixture-private-token-0987654321 after")
        with patch.object(sys, "argv", ["check_privacy", self.directory.name]), patch.object(
                check_privacy, "collect_values", return_value=({"fixture-private-token-0987654321"}, True)):
            self.assertEqual(check_privacy.main(), 1)
        self.assertEqual(path.read_text(), "before [redacted] after")
        report = json.loads((path.parent / "artifact-privacy.json").read_text())
        self.assertEqual(report["unexpected_credential_files"], ["leaked.log"])

    def test_skipping_a_required_test_cannot_pass_the_suite(self):
        suite = Suite("unit-light")
        events = json.dumps({"Package": "example", "Test": "required-contract", "Action": "skip"})
        with patch.object(suite, "command", return_value=(0, events)):
            with self.assertRaises(RuntimeError):
                suite.go_tests("mandatory", ["./..."])
        self.assertTrue(suite.failed)

    def test_process_success_without_test_events_cannot_pass_the_suite(self):
        suite = Suite("unit-light")
        with patch.object(suite, "command", return_value=(0, "no tests ran")):
            with self.assertRaises(RuntimeError):
                suite.go_tests("mandatory", ["./..."])

    def test_one_success_cannot_hide_a_missing_required_contract(self):
        suite = Suite("unit-light")
        events = json.dumps({"Package": "example", "Test": "one-contract", "Action": "pass"})
        with patch.object(suite, "command", return_value=(0, events)):
            with self.assertRaises(RuntimeError):
                suite.go_tests("mandatory", ["./..."], required=["one-contract", "missing-contract"])
        self.assertEqual(suite.checks[-1]["actual"], "missing")

    def test_skipped_ansible_contract_cannot_be_reported_as_passed(self):
        suite = Suite("unit-light")
        path = Path(self.directory.name) / "ansible-events.jsonl"
        path.write_text(json.dumps(dict(name="live contract", status="skipped", duration=0)) + "\n")
        suite.report()
        self.assertTrue(suite.failed)
        self.assertEqual(suite.checks[-1]["actual"], "skipped")

    def test_private_container_copies_use_explicit_octal_permissions(self):
        def walk(value):
            if isinstance(value, list):
                for entry in value:
                    walk(entry)
            elif isinstance(value, dict):
                copy = value.get("community.docker.docker_container_copy_into")
                if copy and "mode" in copy:
                    self.assertEqual(copy.get("mode_parse"), "octal_string_only", value.get("name"))
                for entry in value.values():
                    walk(entry)
        for path in COMMON.parent.rglob("*.yaml"):
            if "templates" not in path.parts:
                walk(yaml.safe_load(path.read_text()))

    def test_cleanup_failure_cannot_leave_a_green_test_report(self):
        spec = importlib.util.spec_from_file_location("launcher", COMMON.parent / "run.py")
        launcher = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(launcher)
        suite = Suite("unit-light")
        suite.check("all assertions passed", True)
        suite.report()
        launcher.record_failure(Path(self.directory.name), "cleanup", "Docker refused to remove the run volume")
        report = json.loads((Path(self.directory.name) / "result.json").read_text())
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["checks"][-1]["id"], "cleanup")
        self.assertEqual(ET.parse(Path(self.directory.name) / "junit.xml").getroot().get("failures"), "1")

    def test_main_push_and_test_changes_run_the_publication_gates(self):
        workflow = yaml.load((COMMON.parents[1] / ".github/workflows/image.yaml").read_text(), Loader=yaml.BaseLoader)
        self.assertIn("**", workflow["on"]["push"]["branches"])
        for event in ("push", "pull_request"):
            self.assertIn("test/**", workflow["on"][event]["paths"])
        self.assertEqual(set(workflow["jobs"]["image"]["needs"]), {"static", "light", "heavy", "superheavy"})
        for job in ("static", "light", "heavy", "superheavy"):
            self.assertEqual(workflow["jobs"][job]["uses"], "./.github/workflows/test-suite.yaml")

    def test_nodes_never_share_the_host_cgroup_namespace(self):
        tasks = yaml.safe_load((COMMON / "tasks/cluster.yaml").read_text())
        nodes = [task["community.docker.docker_container"] for task in tasks if "community.docker.docker_container" in task]
        self.assertTrue(nodes)
        for node in nodes:
            self.assertEqual(node["cgroupns_mode"], "private")
            self.assertFalse(node.get("published_ports"))


if __name__ == "__main__":
    unittest.main()

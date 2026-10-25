"""Container entry point, bounded subprocesses and durable test reports."""
import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
import traceback
import xml.etree.ElementTree as ET

ROOT = Path("/workspace")


class Suite:
    def __init__(self, name):
        self.name = name
        self.artifacts = Path(os.environ["KUBERPACK_TEST_ARTIFACTS"])
        self.run_id = os.environ["KUBERPACK_TEST_RUN"]
        self.started = time.monotonic()
        self.checks = []
        self.secret_values = set()
        self.failed = False
        self.phase = "prepare"
        self.budget_start = None
        config_path = ROOT / "test" / ("static" if name == "static" else "unit/" + name) / "suite.json"
        self.config = json.loads(config_path.read_text()) if name != "prepare" else {"budget_seconds": 1200}
        self.environment = dict(os.environ, GOMAXPROCS="2", GOFLAGS="-p=2", HOME="/tmp/kuberpack-home",
                                KUBERPACK_TEST_BIN="/tmp/kuberpack-bin")
        Path(self.environment["HOME"]).mkdir(exist_ok=True)
        Path(self.environment["KUBERPACK_TEST_BIN"]).mkdir(exist_ok=True)
        (Path(self.environment["HOME"]) / ".ansible/tmp").mkdir(parents=True, exist_ok=True, mode=0o700)

    def redact(self, text):
        for value in sorted(self.secret_values, key=len, reverse=True):
            if value:
                text = text.replace(value, "[redacted]")
        return text

    def check(self, name, passed, duration=0, evidence="", expected="success", actual=None):
        self.checks.append(dict(id=name, status="passed" if passed else "failed", duration=round(duration, 4),
                                phase=self.phase, expected=expected, actual=actual,
                                evidence=self.redact(str(evidence))))
        if not passed:
            self.failed = True

    def remaining(self):
        if self.budget_start is None:
            return 1200
        return max(0.1, self.config["budget_seconds"] - (time.monotonic() - self.budget_start))

    def command(self, name, argv, timeout=60, env=None, check=True):
        start = time.monotonic()
        print(name, flush=True)
        process = subprocess.Popen(argv, cwd=ROOT, env=dict(self.environment, **(env or {})),
                                   stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
                                   start_new_session=True)
        try:
            output, _ = process.communicate(timeout=min(timeout, self.remaining()))
            code = process.returncode
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                output, _ = process.communicate(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                output, _ = process.communicate()
            output += "\nCommand exceeded its deadline.\n"
            code = 124
        duration = time.monotonic() - start
        log = self.artifacts / (str(len(self.checks)).zfill(3) + "-" + name.replace("/", "-").replace(" ", "-") + ".log")
        log.write_text(self.redact(output))
        self.check(name, code == 0, duration, log.name, actual=code)
        if code and check:
            print(self.redact(output[-12000:]), file=sys.stderr)
            raise RuntimeError(f"{name} failed with exit {code}; see {log.name}")
        return code, output

    def go_tests(self, name, args, env=None, allowed_skips=()):
        code, output = self.command(name, ["go", "test", "-json", "-count=1", "-shuffle=on", *args],
                                    timeout=1800, env=env, check=False)
        terminal = 0
        for line in output.splitlines():
            try:
                item = json.loads(line)
            except ValueError:
                continue
            if not item.get("Test") or item.get("Action") not in ("pass", "fail", "skip"):
                continue
            terminal += 1
            skipped = item["Action"] == "skip"
            permitted = skipped and item["Test"] in allowed_skips
            self.check(item["Package"] + "/" + item["Test"], item["Action"] == "pass" or permitted,
                       item.get("Elapsed", 0), f"{name}: {item['Action']}",
                       expected="explicit opt-in excluded" if permitted else "pass", actual=item["Action"])
            if permitted:
                self.checks[-1]["status"] = "skipped"
        if code or not terminal or self.failed:
            print(self.redact(output[-16000:]), file=sys.stderr)
            raise RuntimeError(f"{name} failed or skipped a required test")

    def ansible(self, playbook, extra=None, timeout=1800):
        variables = dict(run_id=self.run_id, artifacts=str(self.artifacts),
                         images=json.loads((ROOT / "test/common/images.json").read_text()), **(extra or {}))
        path = Path("/tmp/kuberpack-test-variables.json")
        path.write_text(json.dumps(variables))
        path.chmod(0o600)
        try:
            return self.command("ansible " + Path(playbook).stem,
                                ["ansible-playbook", playbook, "--extra-vars", "@" + str(path)], timeout=timeout)
        finally:
            path.unlink(missing_ok=True)

    def report(self):
        release = self.artifacts / "release-checks.jsonl"
        if release.exists():
            for line in release.read_text().splitlines():
                item = json.loads(line)
                self.check(item["id"], item["status"] == "passed", item["duration"], item["evidence"],
                           expected=item["expected"], actual=item["actual"])
        admission = self.artifacts / "admission-tests.jsonl"
        if admission.exists():
            for line in admission.read_text().splitlines():
                item = json.loads(line)
                if item.get("Test") and item["Action"] in ("pass", "fail", "skip"):
                    self.check(item["Test"], item["Action"] == "pass", item.get("Elapsed", 0),
                               admission.name, actual=item["Action"])
        events = self.artifacts / "ansible-events.jsonl"
        if events.exists():
            for line in events.read_text().splitlines():
                item = json.loads(line)
                self.check("ansible/" + item["name"], item["status"] != "failed", item["duration"],
                           events.name, actual=item["status"])
        result = dict(schema_version=1, suite=self.name, run_id=self.run_id,
                      status="failed" if self.failed else "passed", checks=self.checks,
                      budget_seconds=self.config["budget_seconds"],
                      duration_seconds=round(time.monotonic() - self.started, 3),
                      test_seconds=round(time.monotonic() - self.budget_start, 3) if self.budget_start else None)
        result["source_revision"] = subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True, text=True).stdout.strip()
        result["source_dirty"] = bool(subprocess.run(["git", "status", "--porcelain"], cwd=ROOT, capture_output=True, text=True).stdout.strip())
        result["go_version"] = subprocess.run(["go", "version"], capture_output=True, text=True).stdout.strip()
        (self.artifacts / "result.json").write_text(json.dumps(result, indent=2) + "\n")
        junit = ET.Element("testsuite", name=self.name, tests=str(len(self.checks)),
                           failures=str(sum(c["status"] == "failed" for c in self.checks)),
                           skipped=str(sum(c["status"] == "skipped" for c in self.checks)),
                           time=str(result["duration_seconds"]))
        for item in self.checks:
            case = ET.SubElement(junit, "testcase", name=item["id"], classname=item["phase"], time=str(item["duration"]))
            if item["status"] == "failed":
                ET.SubElement(case, "failure", message=f"Expected {item['expected']}").text = item["evidence"]
            if item["status"] == "skipped":
                ET.SubElement(case, "skipped", message="Explicit opt-in excluded from this suite")
            ET.SubElement(case, "system-out").text = item["evidence"]
        ET.ElementTree(junit).write(self.artifacts / "junit.xml", encoding="unicode", xml_declaration=True)
        for item in [self.artifacts, *self.artifacts.rglob("*")]:
            os.chown(item, int(os.environ["KUBERPACK_TEST_UID"]), int(os.environ["KUBERPACK_TEST_GID"]))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("suite")
    parser.add_argument("--skip-prepare", action="store_true")
    args = parser.parse_args()
    suite = Suite(args.suite)
    def interrupted(_signal, _frame):
        raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        if not args.skip_prepare:
            suite.command("download Go modules", ["go", "mod", "download"], timeout=600)
            suite.command("compile Go tests", ["go", "test", "-run=^$", "./..."], timeout=900)
            if args.suite in ("unit-heavy", "unit-superheavy"):
                suite.command("compile race instrumentation", ["go", "test", "-race", "-run=^$", "./..."], timeout=900)
                suite.command("build CLI executables", ["go", "build", "-o", suite.environment["KUBERPACK_TEST_BIN"] + "/", "./cmd/..."], timeout=600)
        if args.suite == "prepare":
            return 0
        suite.phase = "test"
        suite.budget_start = time.monotonic()
        if args.suite == "unit-light":
            suite.go_tests("offline tests", ["-timeout=45s", "./..."], allowed_skips=suite.config["allowed_skips"])
        elif args.suite == "static":
            sys.path.insert(0, str(ROOT / "test/static"))
            from checks import run
            run(suite)
        else:
            if args.suite == "unit-heavy":
                suite.go_tests("race tests", ["-race", "-timeout=60s", "./..."],
                               allowed_skips=json.loads((ROOT / "test/unit/unit-light/suite.json").read_text())["allowed_skips"])
            else:
                suite.go_tests("upstream dependency contracts", ["-timeout=180s", "-run=^(TestTheClusterServicesAreFetchable|TestInfisicalChartsEnforceScopeAndRuntimeHardening|TestResolveDigestOnTheInternet)$", "./internal/platformcli", "./internal/oci"],
                               env={"KUBERPACK_TEST_NETWORK": "1"})
            suite.ansible(str(ROOT / "test/unit" / args.suite / "playbook.yaml"))
        elapsed = time.monotonic() - suite.budget_start
        suite.check("suite time budget", elapsed < suite.config["budget_seconds"], evidence=str(round(elapsed, 3)),
                    expected=f"less than {suite.config['budget_seconds']} seconds", actual=round(elapsed, 3))
    except (Exception, KeyboardInterrupt) as error:
        suite.check("suite completed", False, evidence=str(error), actual=type(error).__name__)
        print(suite.redact(str(error)), file=sys.stderr)
    finally:
        suite.report()
    return int(suite.failed)


if __name__ == "__main__":
    sys.exit(main())

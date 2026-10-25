#!/usr/bin/env python3
"""Run the same containerized test suites locally and in GitHub Actions."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import uuid
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[1]
ALIASES = {"light": "unit-light", "heavy": "unit-heavy", "superheavy": "unit-superheavy"}


def call(argv, **kwargs):
    return subprocess.run(argv, check=True, cwd=ROOT, **kwargs)


def cleanup(run_id):
    """Last-resort cleanup after container termination; never inspect other runs."""
    errors = []
    label = "org.kuberpack.test.run=" + run_id
    for kind, remove in (("container", ["rm", "--force"]), ("network", ["network", "rm"]), ("volume", ["volume", "rm"]), ("image", ["image", "rm"])):
        command = ["docker", kind, "ls", "--quiet", "--filter", "label=" + label]
        found = subprocess.run(command, capture_output=True, text=True, timeout=20)
        if found.returncode:
            errors.append(f"cannot inventory {kind} resources")
            continue
        for identity in found.stdout.split():
            result = subprocess.run(["docker", *remove, identity], capture_output=True, text=True, timeout=30)
            if result.returncode:
                errors.append(f"cannot remove {kind} {identity}")
    if errors:
        raise RuntimeError("; ".join(errors))


def record_failure(artifacts, name, message):
    path = artifacts / "result.json"
    report = json.loads(path.read_text()) if path.exists() else {"schema_version": 1, "checks": []}
    report["status"] = "failed"
    report.setdefault("checks", []).append(dict(id=name, status="failed", expected="success", actual="failed", evidence=message, duration=0))
    path.write_text(json.dumps(report, indent=2) + "\n")
    path = artifacts / "junit.xml"
    tree = ET.parse(path) if path.exists() else ET.ElementTree(ET.Element("testsuite", name="runner", tests="0", failures="0"))
    root = tree.getroot()
    root.set("tests", str(int(root.get("tests", "0")) + 1))
    root.set("failures", str(int(root.get("failures", "0")) + 1))
    ET.SubElement(ET.SubElement(root, "testcase", name=name), "failure").text = message
    tree.write(path, encoding="unicode", xml_declaration=True)


def fallback_diagnostics(run_id, artifacts):
    commands = {
        "timeout-workloads": ["docker", "exec", run_id + "-k3s", "/bin/kubectl", "get", "pods,deployments,jobs", "-A", "-o=wide", "--request-timeout=10s"],
        "timeout-events": ["docker", "exec", run_id + "-k3s", "/bin/kubectl", "get", "events", "-A", "--sort-by=.metadata.creationTimestamp", "--request-timeout=10s"],
    }
    for name, command in commands.items():
        try:
            result = subprocess.run(command, capture_output=True, text=True, timeout=15)
            (artifacts / (name + ".log")).write_text(result.stdout + result.stderr)
        except subprocess.SubprocessError as error:
            (artifacts / (name + ".log")).write_text(type(error).__name__)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("suite", choices=["prepare", "static", "light", "heavy", "superheavy", *ALIASES.values()])
    parser.add_argument("--artifacts", type=Path)
    parser.add_argument("--image", help="use an already built toolbox image")
    parser.add_argument("--cache-dir", type=Path, help="bind a Go cache directory, e.g. for actions/cache")
    parser.add_argument("--skip-prepare", action="store_true", help="skip compilation warmup; its cost counts toward the suite budget")
    args = parser.parse_args()
    suite = ALIASES.get(args.suite, args.suite)
    run_id = "kpt-" + uuid.uuid4().hex[:12]
    artifacts = (args.artifacts or ROOT / ".test-results" / run_id).resolve()
    if artifacts.exists() and any(artifacts.iterdir()):
        parser.error("--artifacts must designate a new or empty directory")
    artifacts.mkdir(parents=True, exist_ok=True, mode=0o700)
    started = time.monotonic()
    status = 1
    try:
        call(["docker", "info"], stdout=subprocess.DEVNULL)
        image = args.image
        if not image:
            common = ROOT / "test/common"
            digest = hashlib.sha256(b"".join((common / name).read_bytes() for name in ("Dockerfile", "requirements.txt", "collections.yaml"))).hexdigest()[:16]
            image = "kuberpack-test-tools:" + digest
            call(["docker", "build", "--platform=linux/amd64", "--tag", image, str(common)], timeout=900)
        command = ["docker", "run", "--rm", "--name", run_id + "-runner", "--platform=linux/amd64",
                   "--label", "org.kuberpack.test.run=" + run_id,
                   "--mount", f"type=bind,source={ROOT},target=/workspace,readonly",
                   "--mount", f"type=bind,source={artifacts},target=/artifacts",
                   "--env", "CI=true", "--env", "KUBERPACK_TEST_RUN=" + run_id,
                   "--env", "KUBERPACK_TEST_ARTIFACTS=/artifacts",
                   "--env", "KUBERPACK_TEST_UID=" + str(os.getuid()),
                   "--env", "KUBERPACK_TEST_GID=" + str(os.getgid())]
        if args.cache_dir:
            args.cache_dir = args.cache_dir.resolve()
            args.cache_dir.mkdir(parents=True, exist_ok=True)
            command += ["--mount", f"type=bind,source={args.cache_dir},target=/cache"]
        else:
            command += ["--mount", "type=volume,source=kuberpack-test-go1269,target=/cache"]
        if suite in ("unit-heavy", "unit-superheavy"):
            command += ["--mount", "type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock"]
        command += [image, suite]
        if args.skip_prepare:
            command.append("--skip-prepare")
        process = subprocess.Popen(command, cwd=ROOT)
        try:
            status = process.wait(timeout=3600 if suite == "unit-superheavy" else 1800)
        except (KeyboardInterrupt, subprocess.TimeoutExpired):
            subprocess.run(["docker", "stop", "--time", "20", run_id + "-runner"], capture_output=True, timeout=30)
            process.wait(timeout=30)
            status = 130
    except (subprocess.SubprocessError, OSError, RuntimeError) as error:
        print(str(error), file=sys.stderr)
    finally:
        if suite in ("unit-heavy", "unit-superheavy"):
            if status and not (artifacts / "diagnostics-collection.json").exists():
                fallback_diagnostics(run_id, artifacts)
            try:
                cleanup(run_id)
            except (subprocess.SubprocessError, OSError, RuntimeError) as error:
                print("Cleanup failed: " + str(error), file=sys.stderr)
                status = 1
                record_failure(artifacts, "disposable resource cleanup", str(error))
        metadata = {"run_id": run_id, "suite": suite, "exit_code": status,
                    "total_seconds": round(time.monotonic() - started, 3)}
        (artifacts / "invocation.json").write_text(json.dumps(metadata, indent=2) + "\n")
        if not (artifacts / "result.json").exists():
            (artifacts / "result.json").write_text(json.dumps(dict(metadata, status="failed", error="Runner did not produce a report"), indent=2) + "\n")
            (artifacts / "junit.xml").write_text('<testsuite name="runner" tests="1" failures="1"><testcase name="startup"><failure>Runner did not produce a report</failure></testcase></testsuite>\n')
        elif status and json.loads((artifacts / "result.json").read_text()).get("status") != "failed":
            record_failure(artifacts, "container invocation", f"Runner exited with status {status}")
        print("Artifacts: " + str(artifacts))
    return status


if __name__ == "__main__":
    sys.exit(main())

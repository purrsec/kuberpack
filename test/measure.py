#!/usr/bin/env python3
"""Repeat complete Docker suites and retain every failure and measured duration."""
import argparse
import json
from pathlib import Path
import statistics
import subprocess
import sys
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("suite", choices=["static", "light", "heavy", "superheavy"])
    parser.add_argument("--runs", type=int, default=10)
    parser.add_argument("--image")
    args = parser.parse_args()
    if args.runs < 1:
        parser.error("--runs must be positive")
    root = Path(__file__).resolve().parents[1]
    destination = root / ".test-results" / ("measure-" + uuid.uuid4().hex[:12])
    destination.mkdir(parents=True, mode=0o700)
    samples = []
    try:
        for index in range(args.runs):
            artifacts = destination / f"run-{index + 1:03}"
            command = [sys.executable, str(root / "test/run.py"), args.suite, "--artifacts", str(artifacts)]
            if args.image:
                command += ["--image", args.image]
            with (destination / f"run-{index + 1:03}.log").open("w") as output:
                result = subprocess.run(command, stdout=output, stderr=subprocess.STDOUT)
            report = json.loads((artifacts / "result.json").read_text())
            samples.append(dict(run=index + 1, exit_code=result.returncode,
                                test_seconds=report.get("test_seconds"), status=report["status"]))
            print(json.dumps(samples[-1]), flush=True)
    finally:
        durations = [item["test_seconds"] for item in samples if item["test_seconds"] is not None]
        summary = dict(suite=args.suite, requested_runs=args.runs, completed_runs=len(samples), samples=samples,
                       failures=sum(item["status"] != "passed" or item["exit_code"] != 0 for item in samples),
                       p50=statistics.median(durations) if durations else None,
                       p95=statistics.quantiles(durations, n=100, method="inclusive")[94] if len(durations) > 1 else None)
        (destination / "measurements.json").write_text(json.dumps(summary, indent=2) + "\n")
        print("Measurements: " + str(destination))
    return int(summary["failures"] > 0 or len(samples) != args.runs)


if __name__ == "__main__":
    sys.exit(main())

"""Static contracts evaluated in the shared tool container."""
import ast
import json
from pathlib import Path
import subprocess
import sys

import yaml


def run(suite):
    root = Path("/workspace")
    _, files = suite.command("list Go sources", ["git", "ls-files", "--cached", "--others", "--exclude-standard", "*.go"])
    _, formatted = suite.command("Go formatting", ["gofmt", "-l", *files.splitlines()])
    if formatted.strip():
        suite.check("Go formatting clean", False, evidence=formatted)
        raise RuntimeError("Run gofmt on the listed files")
    suite.command("Go vet", ["go", "vet", "./..."], timeout=60)
    suite.command("release manifest contract", ["go", "run", "./cmd/kuberpack", "validate", "--file", "test/common/fixtures/source-project.yaml", "--output=json"])
    suite.command("GitHub workflow validation", ["actionlint", "-shellcheck=", "-pyflakes="])
    for tier in ("unit-heavy", "unit-superheavy"):
        suite.command("Ansible syntax " + tier, ["ansible-playbook", "--syntax-check", "test/unit/" + tier + "/playbook.yaml"])
    for chart in ("kuberpack", "stateless"):
        image = json.loads((root / "test/common/images.json").read_text())["busybox"]
        suite.command("Helm lint " + chart, ["helm", "lint", "--strict", "provisioning/charts/" + chart, "--set-string", "image=" + image, "--set-string", "hostname=lint.fixture.test"])
    for path in sorted((root / "test").rglob("*.py")):
        ast.parse(path.read_text(), filename=str(path))
    for path in sorted((root / "test").rglob("*.yaml")):
        if "templates" in path.parts:
            continue
        yaml.safe_load(path.read_text())
    suite.command("external fixture chart", ["helm", "lint", "--strict", "test/common/fixtures/external-chart", "--set-string", "image=" + image])
    suite.command("test harness contracts", [sys.executable, "-m", "unittest", "discover", "-s", "test/static", "-p", "test_*.py"], timeout=20)

"""Static contracts evaluated in the shared tool container."""
import ast
import json
from pathlib import Path
import re
import subprocess
import sys

import yaml

# Dockerfiles whose Go toolchain must track go.mod. Dependabot only bumps
# literal FROM lines, never ARG defaults, so an ARG GO_VERSION left behind
# breaks the control image build with "go.mod requires go >= ...".
GO_DOCKERFILES = ("Dockerfile", "Dockerfile.builder", "test/common/Dockerfile")


def go_directive(text):
    """Return the version of the go directive, ignoring comments."""
    for raw in text.splitlines():
        line = raw.split("#", 1)[0].strip()
        if line == "go" or line.startswith("go ") or line.startswith("go\t"):
            parts = line.split()
            if len(parts) == 2:
                return parts[1]
    return ""


def docker_go_pins(text):
    """List the (kind, version) Go pins of a Dockerfile, ignoring comments.

    Variable references such as ${GO_VERSION} are skipped: they are covered
    by the ARG check.
    """
    pins = []
    for raw in text.splitlines():
        line = raw.split("#", 1)[0].strip()
        if line.startswith("ARG ") or line.startswith("ARG\t"):
            name, _, value = line[3:].strip().partition("=")
            if name.strip() == "GO_VERSION" and value.strip():
                pins.append(("ARG GO_VERSION", value.strip()))
        elif line.startswith("FROM ") or line.startswith("FROM\t"):
            match = re.match(r"FROM\s+(?:--\S+\s+)*golang:([0-9][A-Za-z0-9._+~-]*)", line)
            if match:
                version = re.split(r"[-@+~]", match.group(1), maxsplit=1)[0]
                pins.append(("FROM golang", version))
    return pins


def go_toolchain_errors(root):
    """Report Go version drift between go.mod and the Dockerfiles."""
    try:
        wanted = go_directive((root / "go.mod").read_text())
    except OSError as error:
        return ["go.mod is unreadable: %s" % error]
    if not wanted:
        return ["go.mod has no go directive"]
    errors = []
    for name in GO_DOCKERFILES:
        try:
            pins = docker_go_pins((root / name).read_text())
        except OSError as error:
            errors.append("%s is unreadable: %s" % (name, error))
            continue
        if not pins:
            errors.append("%s pins no Go version (set ARG GO_VERSION or a literal FROM golang)" % name)
            continue
        for kind, version in pins:
            if version.startswith("$"):
                continue
            if version != wanted:
                errors.append("%s: %s must match go.mod (go %s)" % (name, kind, wanted))
    return errors


def run(suite):
    root = Path("/workspace")
    _, files = suite.command("list Go sources", ["git", "ls-files", "--cached", "--others", "--exclude-standard", "*.go"])
    _, formatted = suite.command("Go formatting", ["gofmt", "-l", *files.splitlines()])
    if formatted.strip():
        suite.check("Go formatting clean", False, evidence=formatted)
        raise RuntimeError("Run gofmt on the listed files")
    suite.command("Go vet", ["go", "vet", "./..."], timeout=60)
    toolchain = go_toolchain_errors(root)
    if toolchain:
        suite.check("Go toolchain matches go.mod", False, evidence="\n".join(toolchain))
        raise RuntimeError("Dockerfile: ARG GO_VERSION must match go.mod (go %s)" % go_directive((root / "go.mod").read_text()))
    suite.command("release manifest contract", ["go", "run", "./cmd/kuberpack", "validate", "--file", "test/common/fixtures/source-project.yaml", "--output=json"])
    suite.command("GitHub workflow validation", ["actionlint", "-shellcheck=", "-pyflakes="])
    for tier in ("unit-heavy", "unit-superheavy"):
        suite.command("Ansible syntax " + tier, ["ansible-playbook", "--syntax-check", "test/unit/" + tier + "/playbook.yaml"])
    for chart in ("kuberpack", "stateless"):
        image = json.loads((root / "test/common/images.json").read_text())["busybox"]
        suite.command("Helm lint " + chart, ["helm", "lint", "--strict", "provisioning/charts/" + chart, "--set-string", "image=" + image, "--set-string", "hostname=lint.fixture.test"])
    for path in sorted((root / "test").rglob("*.py")):
        ast.parse(path.read_text(), filename=str(path))
    for path in sorted((root / "test").rglob("*.json")):
        json.loads(path.read_text())
    for path in sorted((root / "test").rglob("*.yaml")):
        if "templates" in path.parts:
            continue
        yaml.safe_load(path.read_text())
    suite.command("external fixture chart", ["helm", "lint", "--strict", "test/common/fixtures/external-chart", "--set-string", "image=" + image])
    suite.command("test harness contracts", [sys.executable, "-m", "unittest", "discover", "-s", "test/static", "-p", "test_*.py"], timeout=20)

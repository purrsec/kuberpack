"""Invoke the product installer with isolated credentials and a redacted log."""
import json
import os
from pathlib import Path
import subprocess
import sys


def install(directory, artifacts, attempt):
    directory, artifacts = Path(directory), Path(artifacts)
    values = json.loads((directory / "credentials.json").read_text())
    environment = dict(os.environ,
                       KUBERPACK_GIT_TOKEN=values["forge_token"],
                       KUBERPACK_REGISTRY_TOKEN=values["registry_token"],
                       KUBERPACK_BUILDER_GIT_TOKEN=values["builder_token"],
                       KUBERPACK_BOOTSTRAP_API_TOKEN=values["api_token"],
                       SSL_CERT_FILE=str(directory / "bundle.crt"))
    context = subprocess.run(["kubectl", "config", "current-context"], check=True, capture_output=True, text=True, timeout=15).stdout.strip()
    args = [str(Path(os.environ["KUBERPACK_TEST_BIN"]) / "kuberpack-admin"), "install", "--context", context, "--yes", "--config", str(directory / "platform.yaml"),
            "--ca-file", str(directory / "bundle.crt")]
    result = subprocess.run(args, env=environment, capture_output=True, text=True, timeout=900)
    output = result.stdout + result.stderr
    for name, value in values.items():
        if name.endswith("token"):
            output = output.replace(value, "[redacted]")
    (artifacts / (attempt + "-installation.log")).write_text(output)
    if result.returncode:
        raise RuntimeError(f"installer failed with exit {result.returncode}; see {attempt}-installation.log")


if __name__ == "__main__":
    install(*sys.argv[1:])

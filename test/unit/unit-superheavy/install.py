"""Invoke the product installer with isolated credentials and a redacted log."""
import json
import hashlib
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time


def bootstrap_fingerprint(environment):
    result = {}
    for namespace, names in (("kuberpack", ["kuberpack"]), ("infisical", ["infisical-secrets", "infisical-bootstrap-credentials", "infisical-database-credentials"])):
        for name in names:
            command = subprocess.run(["kubectl", "get", "secret", name, "-n", namespace, "-o=json"],
                                     env=environment, capture_output=True, text=True, timeout=15, check=True)
            obj = json.loads(command.stdout)
            state = {"uid": obj["metadata"]["uid"], "data": obj["data"]}
            result[namespace + "/" + name] = hashlib.sha256(json.dumps(state, sort_keys=True).encode()).hexdigest()
    return result


def invoke(args, environment, artifacts, name, values, interrupt=False):
    # An unlinked private file avoids both a full stdout pipe and an artifact
    # containing unredacted bootstrap output if the runner is interrupted.
    with tempfile.TemporaryFile(mode="w+") as log:
        process = subprocess.Popen(args, env=environment, stdout=log, stderr=subprocess.STDOUT)
        try:
            if interrupt:
                deadline = time.monotonic() + 600
                while time.monotonic() < deadline:
                    if process.poll() is not None:
                        raise RuntimeError("installer exited before the interruption checkpoint")
                    pending = subprocess.run(["kubectl", "get", "secrets", "-n", "infisical", "-l=owner=helm", "-o=json", "--request-timeout=5s"],
                                             env=environment, capture_output=True, text=True, timeout=10)
                    if pending.returncode == 0 and any(obj["metadata"].get("labels", {}).get("status") == "pending-install"
                                                       for obj in json.loads(pending.stdout)["items"]):
                        process.send_signal(signal.SIGINT)
                        if process.wait(timeout=30) == 0:
                            raise RuntimeError("interrupted installation unexpectedly reported success")
                        return
                    time.sleep(1)
                raise TimeoutError("installation interruption checkpoint was not reached")
            code = process.wait(timeout=900)
            if code:
                raise RuntimeError(f"installer failed with exit {code}; see {name}-installation.log")
        finally:
            if process.poll() is None:
                process.kill()
                process.wait(timeout=15)
            log.seek(0)
            output = log.read()
            for key, value in values.items():
                if key.endswith("token"):
                    output = output.replace(value, "[redacted]")
            (artifacts / (name + "-installation.log")).write_text(output)


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
    before = None
    if attempt == "first":
        invoke(args, environment, artifacts, "interrupted", values, interrupt=True)
        before = bootstrap_fingerprint(environment)
    invoke(args, environment, artifacts, attempt, values)
    if before is not None:
        preserved = bootstrap_fingerprint(environment) == before
        with (artifacts / "release-checks.jsonl").open("a") as stream:
            stream.write(json.dumps(dict(id="interruption recovery preserves bootstrap credentials", status="passed" if preserved else "failed",
                                        expected=True, actual=preserved, evidence="credential UID/data fingerprints before and after resume", duration=0)) + "\n")
        if not preserved:
            raise RuntimeError("bootstrap credentials changed while resuming the interrupted installation")


if __name__ == "__main__":
    install(*sys.argv[1:])

"""Collect failure evidence from this run only, redacting fixture credentials."""
import json
from pathlib import Path
import subprocess
import sys
import time

from privacy import collect_values, redact as clean


def main():
    run_id, artifacts = sys.argv[1:]
    artifacts = Path(artifacts)
    try:
        values, redaction_ready = collect_values()
    except (subprocess.SubprocessError, ValueError):
        values, redaction_ready = set(), False

    def redact(text):
        return clean(text, values)

    records = []

    def collect(name, args):
        started = time.monotonic()
        try:
            result = subprocess.run(args, capture_output=True, text=True, timeout=15)
            output = result.stdout + result.stderr
            code = result.returncode
        except subprocess.SubprocessError as error:
            code, output = 124, type(error).__name__
        (artifacts / (name + ".log")).write_text(redact(output))
        records.append(dict(name=name, exit_code=code, duration=round(time.monotonic() - started, 3)))

    for container in ("k3s", "forgejo"):
        collect("docker-" + container, ["docker", "logs", "--tail=150", run_id + "-" + container])
    for namespace, label in (("kuberpack", "app.kubernetes.io/part-of=kuberpack"),
                             ("flux-system", "app.kubernetes.io/part-of=flux"),
                             ("kuberpack-build", "app.kubernetes.io/name=kuberpack-build"),
                             ("pipeline", "app.kubernetes.io/part-of=kuberpack")):
        if not redaction_ready:
            records.append(dict(name="logs-" + namespace, status="omitted", reason="Cannot redact cluster credentials"))
            continue
        collect("logs-" + namespace, ["kubectl", "logs", "-n", namespace, "-l", label,
                "--all-containers=true", "--prefix=true", "--tail=100", "--max-log-requests=20", "--request-timeout=10s"])
    for kind in ("helmreleases.helm.toolkit.fluxcd.io", "kustomizations.kustomize.toolkit.fluxcd.io", "gitrepositories.source.toolkit.fluxcd.io"):
        collect(kind.split(".")[0], ["kubectl", "get", kind, "-A", "-o=json", "--request-timeout=10s"])
    (artifacts / "diagnostics-collection.json").write_text(json.dumps(records, indent=2) + "\n")


if __name__ == "__main__":
    main()

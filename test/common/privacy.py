"""Redaction shared by diagnostics and the final artifact privacy assertion."""
import base64
import json
from pathlib import Path
import re
import subprocess


def collect_values():
    values = set()
    private = Path("/tmp/kuberpack-fixture/credentials.json")
    if private.exists():
        values.update(v for k, v in json.loads(private.read_text()).items() if k.endswith("token"))
    kubeconfig = Path("/tmp/kuberpack-kubeconfig.yaml")
    if not kubeconfig.exists():
        return values, True
    result = subprocess.run(["kubectl", "get", "secrets", "-A", "-o=json", "--request-timeout=5s"],
                            capture_output=True, text=True, timeout=8)
    if result.returncode:
        return values, False
    for item in json.loads(result.stdout)["items"]:
        credential = "credentials" in item["metadata"]["name"]
        for key, encoded in item.get("data", {}).items():
            if not credential and not any(word in key.lower() for word in ("password", "token", "secret", "key", "auth", "dockerconfig")):
                continue
            values.add(encoded)
            try:
                values.add(base64.b64decode(encoded).decode())
            except (ValueError, UnicodeError):
                pass
    for value in list(values):
        values.add(base64.b64encode(value.encode()).decode())
        values.add(base64.b64encode(("kuberpack:" + value).encode()).decode())
    return {value for value in values if len(value) >= 12}, True


def redact(text, values):
    for value in sorted(values, key=len, reverse=True):
        text = text.replace(value, "[redacted]")
    return re.sub(r"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", "[redacted-jwt]", text)

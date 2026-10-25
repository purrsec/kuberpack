"""Seed a new private forge. Fixture secrets stay in a private temporary file."""
import base64
import json
import os
from pathlib import Path
import secrets
import ssl
import subprocess
import sys
import urllib.request


def main():
    run_id, forge_ip, directory = sys.argv[1:]
    directory = Path(directory)
    password = secrets.token_urlsafe(32)
    result = subprocess.run([
        "docker", "exec", "--user=1000:1000", run_id + "-forgejo", "/usr/local/bin/forgejo",
        "admin", "user", "create", "--config=/var/lib/gitea/fixture/app.ini",
        "--username=kuberpack", "--email=runner@fixture.test", "--password=" + password, "--admin", "--must-change-password=false"],
        capture_output=True, text=True, timeout=30)
    if result.returncode:
        raise RuntimeError(f"fixture user creation failed, exit={result.returncode}: " + (result.stdout + result.stderr).replace(password, "[redacted]"))
    origin = "https://" + forge_ip + ":3000"
    context = ssl.create_default_context(cafile=str(directory / "ca.crt"))

    def request(path, data, auth):
        req = urllib.request.Request(origin + "/api/v1" + path, json.dumps(data).encode(),
                                     {"Content-Type": "application/json", "Authorization": auth})
        with urllib.request.urlopen(req, context=context, timeout=30) as response:
            if response.status != 201:
                raise RuntimeError(f"forge creation returned HTTP {response.status}")
            return json.load(response)

    basic = "Basic " + base64.b64encode(("kuberpack:" + password).encode()).decode()
    values = {"forge_url": origin, "api_token": secrets.token_urlsafe(40)}
    for name in ("forge_token", "registry_token", "builder_token"):
        values[name] = request("/users/kuberpack/tokens", {"name": name, "scopes": ["all"]}, basic)["sha1"]
    for name in ("platform", "applications", "source"):
        request("/user/repos", {"name": name, "private": True, "auto_init": True, "default_branch": "main"},
                "token " + values["forge_token"])
    path = directory / "credentials.json"
    with open(path, "x", opener=lambda p, flags: os.open(p, flags, 0o600)) as stream:
        json.dump(values, stream)


if __name__ == "__main__":
    main()

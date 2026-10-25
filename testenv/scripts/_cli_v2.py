"""API-v2 setup helpers for the disposable fixture; never prints credentials."""

import argparse
import json
import os
from pathlib import Path
import ssl
import subprocess
import time
import urllib.error
import urllib.request


class FixtureAPI:
    def __init__(self, base, administrator, ca_file):
        if not base.startswith("https://"):
            raise ValueError("the fixture API requires HTTPS")
        self.base = base.rstrip("/")
        self.administrator = administrator
        self.ca_file = str(ca_file)
        self.context = ssl.create_default_context(cafile=self.ca_file)

    def request(self, method, path, data=None):
        body = None if data is None else json.dumps(data).encode()
        request = urllib.request.Request(
            self.base + path, body,
            {"Authorization": "Bearer " + self.administrator,
             "Content-Type": "application/json"}, method=method)
        try:
            with urllib.request.urlopen(request, context=self.context, timeout=120) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            raise RuntimeError(f"fixture API {method} {path}: HTTP {error.code}") from None

    def developer_environment(self, environment, project, policy):
        try:
            self.request("POST", "/api/v2/admin/projects/" + project)
        except RuntimeError as error:
            if "HTTP 409" not in str(error):
                raise
        policy = dict(namespace=project, max_services=20, max_replicas=10, **policy)
        self.request("PUT", "/api/v2/admin/projects/" + project + "/policy", policy)
        token = self.request("POST", "/api/v2/admin/tokens", {
            "name": "fixture-" + project + "-" + str(time.time_ns()),
            "role": "maintainer", "project": project, "expires_in": "2h"})["token"]
        result = dict(environment, KUBERPACK_API_URL=self.base, KUBERPACK_API_TOKEN=token)
        result["SSL_CERT_FILE"] = self.ca_file
        return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--project", required=True)
    parser.add_argument("--file", required=True)
    parser.add_argument("--api", required=True)
    parser.add_argument("--ca-file", required=True)
    parser.add_argument("--repository", action="append", default=[])
    parser.add_argument("--domain", action="append", default=[])
    args = parser.parse_args()
    fixture = FixtureAPI(args.api, os.environ["KUBERPACK_TEST_ADMIN_TOKEN"], args.ca_file)
    developer = fixture.developer_environment(os.environ, args.project, {
        "repositories": args.repository, "domains": args.domain})
    cli = Path(__file__).resolve().parents[2] / "bin/kuberpack"
    result = subprocess.run([str(cli), "deploy", "--file", args.file,
                             "--yes", "--non-interactive", "--detach", "--output", "json"],
                            env=developer, capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError(f"fixture deployment failed with exit {result.returncode}")
    print(result.stdout.strip())


if __name__ == "__main__":
    main()

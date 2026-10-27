"""Typed release assertions against the actual installed API, GitOps and pods."""
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request
import yaml

from install import install


class Contracts:
    def __init__(self, directory, artifacts):
        self.directory, self.artifacts = Path(directory), Path(artifacts)
        self.credentials = json.loads((self.directory / "credentials.json").read_text())
        self.secrets = {v for k, v in self.credentials.items() if k.endswith("token")}
        self.context = ssl.create_default_context(cafile=str(self.directory / "bundle.crt"))
        self.api = "https://kuberpack.fixture.test"
        self.environment = dict(os.environ, SSL_CERT_FILE=str(self.directory / "bundle.crt"),
                                GIT_SSL_CAINFO=str(self.directory / "bundle.crt"),
                                INFISICAL_DOMAIN="https://infisical.fixture.test")
        self.token = self.credentials["api_token"]

    def redact(self, text):
        for value in sorted(self.secrets, key=len, reverse=True):
            text = text.replace(value, "[redacted]")
        return re.sub(r"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", "[redacted-jwt]", text)

    def check(self, name, actual, expected):
        passed = actual == expected
        event = dict(id=name, status="passed" if passed else "failed", expected=expected, actual=actual,
                     evidence="actual API/Kubernetes response", duration=0)
        with (self.artifacts / "release-checks.jsonl").open("a") as stream:
            stream.write(self.redact(json.dumps(event)) + "\n")
        if not passed:
            raise AssertionError(self.redact(f"{name}: expected {expected!r}, got {actual!r}"))

    def command(self, name, args, env=None, data=None, timeout=120, private=False):
        started = time.monotonic()
        result = subprocess.run([str(x) for x in args], env=env or self.environment,
                                input=data, capture_output=True, text=True, timeout=timeout)
        if not private or result.returncode:
            with (self.artifacts / "release-commands.log").open("a") as stream:
                output = "[private command output omitted]" if private else result.stdout + result.stderr
                stream.write(self.redact(f"{name}: exit={result.returncode}, seconds={time.monotonic()-started:.3f}\n" + output + "\n"))
        if result.returncode:
            raise RuntimeError(f"{name} failed with exit {result.returncode}; see release-commands.log")
        return result.stdout

    def kube(self, *args, data=None):
        # Resource responses can contain Secret data or literal credentials in
        # pod environments. Only typed observations go to public reports.
        return json.loads(self.command("kubectl " + args[0], ["kubectl", *args, "-o=json"], data=data, private=True))

    def secret(self, name, namespace="infisical"):
        values = {k: base64.b64decode(v).decode() for k, v in self.kube("get", "secret", name, "-n", namespace)["data"].items()}
        self.secrets.update(values.values())
        return values

    def request(self, method, path, data=None, token=None, base=None, expected=200):
        body = None if data is None else json.dumps(data).encode()
        headers = {"Content-Type": "application/json"}
        if token:
            headers["Authorization"] = "Bearer " + token
        request = urllib.request.Request((base or self.api) + path, body, headers, method=method)
        try:
            with urllib.request.urlopen(request, context=self.context, timeout=30) as response:
                status, raw = response.status, response.read()
        except urllib.error.HTTPError as error:
            status, raw = error.code, error.read()
        self.check(method + " " + path + " HTTP status", status, expected)
        return json.loads(raw) if raw else None

    def wait(self, name, predicate, timeout=180):
        end = time.monotonic() + timeout
        last = "condition not observed"
        while time.monotonic() < end:
            try:
                value = predicate()
                if value:
                    self.check(name, True, True)
                    return value
            except (OSError, urllib.error.URLError) as error:
                last = type(error).__name__
            time.sleep(2)
        raise TimeoutError(f"{name} timed out: {last}")

    def cli(self, *args, token=None, timeout=600):
        env = dict(self.environment, KUBERPACK_API_URL=self.api, KUBERPACK_API_TOKEN=token or self.token,
                   KUBECONFIG="/tmp/no-developer-kubernetes-access")
        return json.loads(self.command("kuberpack " + args[0], [Path(os.environ["KUBERPACK_TEST_BIN"]) / "kuberpack", *args, "--non-interactive", "--output=json"], env=env, timeout=timeout))

    def issue_token(self, role, services=None, project="pipeline"):
        value = self.request("POST", "/api/v2/admin/tokens", {
            "name": "fixture-" + role, "role": role, "project": project, "expires_in": "1h",
            "services": services or []}, token=self.credentials["api_token"], expected=201)["token"]
        self.secrets.add(value)
        return value

    def administrator_cli(self, *args):
        env = dict(self.environment, KUBERPACK_API_URL=self.api, KUBERPACK_API_TOKEN=self.credentials["api_token"])
        return json.loads(self.command("administrator " + args[0],
            [Path(os.environ["KUBERPACK_TEST_BIN"]) / "kuberpack-admin", *args, "--non-interactive", "--output=json"], env=env, timeout=180))

    def remote_revision(self, repository):
        output = self.command("read " + repository + " revision", ["git", "ls-remote", self.credentials["forge_url"] + "/kuberpack/" + repository + ".git", "refs/heads/main"], env=self.git_environment())
        return output.split()[0]

    def prepare_secret(self):
        admin = self.secret("infisical-bootstrap-credentials")
        bootstrap = self.secret("infisical-bootstrap-secret")
        identity = self.secret("kubernetes-credentials")["identityId"]
        infisical = "https://infisical.fixture.test"
        project = self.request("POST", "/api/v1/projects", {
            "projectName": "pipeline", "slug": os.environ["KUBERPACK_TEST_RUN"] + "-pipeline",
            "type": "secret-manager", "shouldCreateDefaultEnvs": True},
            token=bootstrap["token"], base=infisical, expected=200)["project"]["id"]
        self.request("POST", f"/api/v1/projects/{project}/memberships/identities/{identity}", {"role": "viewer"},
                     token=bootstrap["token"], base=infisical, expected=200)
        members = self.request("GET", f"/api/v1/projects/{project}/memberships", token=bootstrap["token"], base=infisical)
        if admin["INFISICAL_ADMIN_EMAIL"] not in json.dumps(members):
            self.request("POST", f"/api/v1/projects/{project}/memberships", {
                "usernames": [admin["INFISICAL_ADMIN_EMAIL"]], "roleSlugs": ["admin"]},
                token=bootstrap["token"], base=infisical, expected=200)
        auth = self.kube("get", "infisicalauth", "kuberpack", "-n", "infisical")
        auth["metadata"] = {"name": "kuberpack-pipeline", "namespace": "infisical"}
        auth.pop("status", None)
        self.command("create project operator identity reference", ["kubectl", "apply", "-f", "-"], data=json.dumps(auth))
        self.profile = os.environ["KUBERPACK_TEST_RUN"]
        self.project_id = project
        self.command("human Infisical login", ["infisical", "login", "--method=user", "--email", admin["INFISICAL_ADMIN_EMAIL"],
                     "--password", admin["INFISICAL_ADMIN_PASSWORD"], "--organization-id", bootstrap["organizationId"],
                     "--save-as", self.profile, "--silent"], private=True)
        for folder, parent in (("projects", "/"), ("pipeline", "/projects")):
            self.command("create private secret folder", ["infisical", "secrets", "folders", "create", "--name", folder, "--path", parent,
                         "--env=prod", "--projectId", project, "--profile", self.profile, "--silent"], private=True)
        return project, self.set_secret()

    def set_secret(self):
        value = secrets.token_urlsafe(32)
        self.secrets.add(value)
        path = self.directory / "application-secret.env"
        path.write_text("PIPELINE_SECRET=" + value + "\n")
        path.chmod(0o600)
        self.command("human secret upload", ["infisical", "secrets", "set", "--file", path, "--projectId", self.project_id,
                     "--env=prod", "--path=/projects/pipeline", "--profile", self.profile, "--silent"], private=True)
        return hashlib.sha256(value.encode()).hexdigest()

    def git_environment(self):
        return dict(self.environment, GIT_TERMINAL_PROMPT="0", GIT_CONFIG_COUNT="3",
                    GIT_CONFIG_KEY_0="http.extraHeader", GIT_CONFIG_VALUE_0="Authorization: token " + self.credentials["forge_token"],
                    GIT_CONFIG_KEY_1="user.name", GIT_CONFIG_VALUE_1="Kuberpack Test",
                    GIT_CONFIG_KEY_2="user.email", GIT_CONFIG_VALUE_2="runner@fixture.test")

    def push_source(self, version):
        source = self.directory / "source"
        env = self.git_environment()
        if not source.exists():
            self.command("clone test source", ["git", "clone", self.credentials["forge_url"] + "/kuberpack/source.git", source], env=env)
        program = '''import hashlib, importlib.metadata, importlib.util, json, os
from http.server import BaseHTTPRequestHandler, HTTPServer
BODY = json.dumps({"version": "VERSION", "secret_sha256": hashlib.sha256(os.environ["PIPELINE_SECRET"].encode()).hexdigest(), "pip_absent": importlib.util.find_spec("pip") is None, "certifi_version": importlib.metadata.version("certifi")}).encode()
class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(BODY)
HTTPServer(("0.0.0.0", int(os.environ["PORT"])), Handler).serve_forever()
'''.replace("VERSION", version)
        (source / "main.py").write_text(program)
        (source / ".python-version").write_text("3.13\n")
        # Ship the patched application dependency and omit build-only tooling
        # from the final runtime. The real scanner receives no exception.
        (source / "requirements.txt").write_text("certifi==2026.10.23\n")
        fixture = Path(__file__).resolve().parents[2] / "common/fixtures/railpack.json"
        (source / "railpack.json").write_text(fixture.read_text())
        for args in (["add", "main.py", ".python-version", "requirements.txt", "railpack.json"],
                     ["-c", "commit.gpgsign=false", "commit", "-m", "fixture " + version], ["push", "origin", "main"]):
            self.command("git " + args[0], ["git", "-C", source, *args], env=env)
        return self.command("source revision", ["git", "-C", source, "rev-parse", "HEAD"], env=env).strip()

    def application(self):
        with urllib.request.urlopen("https://web.fixture.test/", context=self.context, timeout=10) as response:
            return json.load(response)

    def api_ready(self):
        try:
            with urllib.request.urlopen(self.api + "/healthz", context=self.context, timeout=5) as response:
                return response.status == 200 and response.read().strip() == b"ok"
        except (OSError, urllib.error.URLError):
            return False

    def fingerprint(self):
        result = {}
        for ns in ("kuberpack", "infisical", "kuberpack-build", "pipeline", "external-apps", "cert-manager"):
            for obj in self.kube("get", "secrets,deployments,configmaps,pvc", "-n", ns)["items"]:
                if obj.get("type") in ("helm.sh/release.v1", "kubernetes.io/tls", "kubernetes.io/service-account-token"):
                    continue
                identity = ns + "/" + obj["kind"] + "/" + obj["metadata"]["name"]
                state = {"uid": obj["metadata"]["uid"], "data": obj.get("data"), "spec": obj.get("spec")}
                result[identity] = hashlib.sha256(json.dumps(state, sort_keys=True).encode()).hexdigest()
        return result

    def pods(self):
        return self.kube("get", "pods", "-n", "pipeline", "-l=app.kubernetes.io/name=web")["items"]

    def flux_ready(self, kind, name, namespace, revision=None):
        resource = self.kube("get", kind, name, "-n", namespace)
        status = resource.get("status", {})
        ready = any(c["type"] == "Ready" and c["status"] == "True"
                    for c in status.get("conditions", []))
        observed = status.get("observedGeneration", 0) >= resource["metadata"]["generation"]
        applied = revision is None or status.get("lastAppliedRevision", "").endswith("@sha1:" + revision)
        return resource if ready and observed and applied else None

    def scoped_rbac(self):
        deployments = self.kube("get", "deployments", "-n", "infisical")["items"]
        operator = next(item for item in deployments if "controller-manager" in item["metadata"]["name"])
        operator_identity = "system:serviceaccount:infisical:" + operator["spec"]["template"]["spec"]["serviceAccountName"]
        control_identity = "system:serviceaccount:kuberpack:kuberpack"
        for name, identity, verb, resource, namespace, expected in (
            ("operator project secrets", operator_identity, "get", "secrets", "pipeline", "yes"),
            ("operator system secrets", operator_identity, "get", "secrets", "kube-system", "no"),
            ("operator platform secrets", operator_identity, "get", "secrets", "kuberpack", "no"),
            ("operator Flux token creation", operator_identity, "create", "serviceaccounts/token", "flux-system", "no"),
            ("control system secrets", control_identity, "get", "secrets", "kube-system", "no"),
            ("control pod execution", control_identity, "create", "pods/exec", "pipeline", "no"),
        ):
            result = subprocess.run(["kubectl", "auth", "can-i", verb, resource, "-n", namespace, "--as", identity],
                                    env=self.environment, capture_output=True, text=True, timeout=20)
            self.check(name + " decision", result.stdout.strip(), expected)
            self.check(name + " exit", result.returncode, 0 if expected == "yes" else 1)
        web = next(item for item in deployments if "standalone-infisical" in item["metadata"]["name"])
        self.check("Infisical web has no mounted API token", web["spec"]["template"]["spec"]["automountServiceAccountToken"], False)

    def run(self):
        self.request("GET", "/api/v2/session", expected=401)
        self.request("POST", "/api/v2/admin/tokens", {"name": "missing-role"}, token=self.token, expected=400)
        project_id, expected_secret = self.prepare_secret()
        commit = self.push_source("one")
        self.request("POST", "/api/v2/admin/projects/pipeline", token=self.token, expected=201)
        self.request("PUT", "/api/v2/admin/projects/pipeline/policy", {
            "namespace": "pipeline", "domains": ["web.fixture.test"], "repositories": ["kuberpack/source"],
            "max_services": 4, "max_replicas": 2, "infisical": {"project_id": project_id}}, token=self.token)
        admin_token = self.token
        self.token = self.issue_token("maintainer")
        reader = self.issue_token("reader")
        for token in (reader, self.token):
            self.request("POST", "/api/v2/projects/pipeline/plans", {"action": "destroy"}, token=token, expected=403)
        self.request("POST", "/api/v2/projects/pipeline/plans", {"action": "deploy"}, token=reader, expected=403)
        fixture = Path(__file__).resolve().parents[2] / "common/fixtures/source-project.yaml"
        manifest = yaml.safe_load(fixture.read_text())
        manifest["infisical"]["project_id"] = project_id
        manifest_path = self.directory / "kuberpack.yaml"
        manifest_path.write_text(json.dumps(manifest, indent=2) + "\n")
        args = ["deploy", "--file", str(manifest_path), "--request-id=fixture-first", "--yes", "--detach"]
        first = self.cli(*args)
        repeated = self.cli(*args)
        self.check("retry returns the accepted deployment", repeated["deployment_id"], first["deployment_id"])
        deployed = self.cli("deployments", "wait", first["deployment_id"], "--timeout=10m", timeout=620)
        self.check("first delivery succeeded", deployed["status"], "succeeded")
        self.check("planned source commit was built", deployed["services"][0]["commit_sha"], commit)
        deployment = self.kube("get", "deployment", "web", "-n", "pipeline")
        image = deployment["spec"]["template"]["spec"]["containers"][0]["image"]
        self.check("runtime image is pinned by digest", re.search(r"@sha256:[a-f0-9]{64}$", image) is not None, True)
        self.check("new generation was observed", deployment["status"]["observedGeneration"] >= deployment["metadata"]["generation"], True)
        self.check("new configuration was observed", deployment["spec"]["template"]["metadata"]["annotations"]["kuberpack.io/configuration-revision"], deployed["services"][0]["configuration_revision"])
        head = self.remote_revision("applications")
        reconciled = self.wait("Flux applied the actual application GitOps revision", lambda:
            self.flux_ready("kustomization", "kuberpack-project-pipeline", "kuberpack", head), timeout=90)
        self.check("Flux uses the project tenant identity", reconciled["spec"]["serviceAccountName"], "kuberpack-tenant-pipeline")
        self.wait("the actual Helm release is ready at its latest generation", lambda:
            self.flux_ready("helmrelease", "web", "pipeline"), timeout=90)
        build_id = deployed["services"][0]["build_id"]
        self.check("a real build was tracked", build_id > 0, True)
        build = self.cli("builds", "show", str(build_id), "--service=web")
        self.check("tracked build succeeded", build["status"], "succeeded")
        self.check("retry did not queue another build", len(self.cli("builds", "list", "--service=web")["builds"]), 1)
        logs = self.command("real builder logs", [Path(os.environ["KUBERPACK_TEST_BIN"]) / "kuberpack", "builds", "logs", str(build_id), "--service=web", "--project=pipeline", "--non-interactive"],
                            env=dict(self.environment, KUBERPACK_API_URL=self.api, KUBERPACK_API_TOKEN=self.token))
        self.check("real scan and SBOM completed", "Trivy passed; Syft SBOM generated" in logs, True)
        body = self.wait("HTTPS application serves the first delivery", lambda: self.application())
        self.check("application version", body["version"], "one")
        self.check("human secret reached the application", body["secret_sha256"], expected_secret)
        self.check("runtime omits the build-only package installer", body["pip_absent"], True)
        self.check("runtime uses the patched TLS dependency", body["certifi_version"], "2026.10.23")
        self.scoped_rbac()
        previous_head = self.remote_revision("applications")
        repeated_configuration = self.cli("deploy", "--file", str(manifest_path), "--request-id=fixture-reapply", "--yes", "--timeout=3m", timeout=200)
        self.check("unchanged configuration converges", repeated_configuration["status"], "succeeded")
        self.check("unchanged configuration creates no GitOps commit", self.remote_revision("applications"), previous_head)
        self.check("unchanged configuration creates no build", len(self.cli("builds", "list", "--service=web")["builds"]), 1)
        self.check("control plane never writes application source", self.remote_revision("source"), commit)
        self.command("restart the actual control plane", ["kubectl", "rollout", "restart", "deployment/kuberpack", "-n=kuberpack"])
        self.command("wait for restarted control plane", ["kubectl", "rollout", "status", "deployment/kuberpack", "-n=kuberpack", "--timeout=120s"], timeout=130)
        self.wait("restarted HTTPS API is routed again", self.api_ready, timeout=60)
        self.check("accepted operation survives control plane restart", self.cli(*args)["deployment_id"], first["deployment_id"])
        self.check("recovery did not queue another build", len(self.cli("builds", "list", "--service=web")["builds"]), 1)
        before_uids = {p["metadata"]["uid"] for p in self.pods()}
        rotated = self.set_secret()
        self.wait("rotation reaches the pod through the real operator and reloader", lambda: self.application().get("secret_sha256") == rotated)
        self.check("secret rotation replaced the pod", any(p["metadata"]["uid"] not in before_uids for p in self.pods()), True)
        next_commit = self.push_source("two")
        self.wait("signed forge webhook builds and deploys the new source", lambda: self.application().get("version") == "two", timeout=420)
        self.check("webhook delivery retains the rotated secret", self.application()["secret_sha256"], rotated)
        builds = self.cli("builds", "list", "--service=web")["builds"]
        self.check("webhook built the pushed commit", any(b["commit_sha"] == next_commit and b["status"] == "succeeded" for b in builds), True)
        webhook_head = self.remote_revision("applications")
        self.wait("Flux applied the webhook GitOps revision before reinstall", lambda:
            self.flux_ready("kustomization", "kuberpack-project-pipeline", "kuberpack", webhook_head), timeout=90)
        self.wait("the webhook Helm release settled before reinstall", lambda:
            self.flux_ready("helmrelease", "web", "pipeline"), timeout=90)
        self.command("create a persistent claim in the project", ["kubectl", "apply", "-f", "-"], data=json.dumps({
            "apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": {"name": "retain-data", "namespace": "pipeline"},
            "spec": {"accessModes": ["ReadWriteOnce"], "resources": {"requests": {"storage": "64Mi"}}}}))
        blocked = self.request("POST", "/api/v2/projects/pipeline/plans", {"action": "destroy"}, token=admin_token, expected=409)
        self.check("administrator destruction is blocked by actual storage", blocked["error"]["code"], "destruction_blocked")
        self.check("storage refusal is based on the live claim", blocked["error"]["message"], "namespace contains persistent claims")
        before = self.fingerprint()
        revisions = (self.remote_revision("platform"), self.remote_revision("applications"))
        install(self.directory, self.artifacts, "reinstall")
        self.check("reinstallation preserves credentials, volumes and workload identities", self.fingerprint(), before)
        self.check("reinstallation creates no GitOps commits", (self.remote_revision("platform"), self.remote_revision("applications")), revisions)
        self.check("reinstalled API remains scoped", self.cli("whoami")["role"], "maintainer")
        self.check("reinstalled delivery remains available", self.application()["version"], "two")
        self.administrator_cli("projects", "create", "remove-me", "--yes")
        revoked = self.issue_token("reader", project="remove-me")
        plan = self.administrator_cli("projects", "destroy", "remove-me", "--plan")
        removed = self.administrator_cli("projects", "destroy", "remove-me", "--plan-id", plan["id"], "--confirm-project=remove-me", "--yes")
        self.check("administrator destroys an empty reservation", removed["status"], "succeeded")
        self.request("GET", "/api/v2/session", token=revoked, expected=401)
        self.third_party_image()


    def third_party_image(self):
        """A published image deploys without a source repository or a build."""
        self.request("POST", "/api/v2/admin/projects/sidekick", token=self.token, expected=201)
        self.request("PUT", "/api/v2/admin/projects/sidekick/policy", {
            "namespace": "sidekick", "domains": ["sidekick.fixture.test"], "repositories": [],
            "max_services": 2, "max_replicas": 1}, token=self.token)
        token = self.issue_token("maintainer", project="sidekick")
        pinned = json.loads((Path(__file__).resolve().parents[2] / "common/images.json").read_text())["busybox"]
        manifest_path = self.directory / "sidekick.yaml"
        manifest_path.write_text(json.dumps({"project": "sidekick", "services": {"side": {
            "image": pinned, "hostname": "sidekick.fixture.test", "port": 8080, "healthcheck": "/",
            "command": ["sh", "-ec", "mkdir -p /tmp/www; printf sidekick-ok > /tmp/www/index.html; exec httpd -f -p 8080 -h /tmp/www"]}}}, indent=2) + "\n")
        args = ["deploy", "--file", str(manifest_path), "--request-id=fixture-sidekick", "--yes", "--detach"]
        first = self.cli(*args, token=token)
        deployed = self.cli("deployments", "wait", first["deployment_id"], "--timeout=10m", token=token, timeout=620)
        self.check("third-party image delivery succeeded", deployed["status"], "succeeded")
        self.check("published image needs no build",
                   len(self.cli("builds", "list", "--service=side", token=token)["builds"]), 0)
        deployment = self.kube("get", "deployment", "side", "-n", "sidekick")
        image = deployment["spec"]["template"]["spec"]["containers"][0]["image"]
        self.check("third-party runtime image is pinned by digest", re.search(r"@sha256:[a-f0-9]{64}$", image) is not None, True)

        def running():
            try:
                items = self.kube("get", "pods", "-n", "sidekick", "-l=app.kubernetes.io/name=side")["items"]
            except RuntimeError:
                return None
            return [p for p in items if p["status"].get("phase") == "Running"] or None
        pods = self.wait("third-party image pod is running", running, timeout=300)
        self.check("third-party pods run the pinned image",
                   all(c["image"] == image for p in pods for c in p["spec"]["containers"]), True)
        plan = self.administrator_cli("projects", "destroy", "sidekick", "--plan")
        removed = self.administrator_cli("projects", "destroy", "sidekick",
                                         "--plan-id", plan["id"], "--confirm-project=sidekick", "--yes")
        self.check("third-party project is destroyed", removed["status"], "succeeded")


def main():
    suite = Contracts(*sys.argv[1:])
    try:
        suite.run()
    except Exception as error:
        with (suite.artifacts / "release-checks.jsonl").open("a") as stream:
            stream.write(json.dumps(dict(id="release completed", status="failed", expected="success",
                                        actual=type(error).__name__, evidence=suite.redact(str(error)), duration=0)) + "\n")
        print(suite.redact(str(error)), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())

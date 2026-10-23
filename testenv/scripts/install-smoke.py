#!/usr/bin/env python3
"""Exercise installation on the disposable Docker k3s fixture, never a VPS."""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import shlex
import shutil
import signal
import ssl
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


ROOT = Path(__file__).resolve().parents[2]


def run(args, env, quiet=False, input=None):
    result = subprocess.run([str(x) for x in args], env=env, capture_output=True, text=True, input=input)
    if result.returncode:
        # CLI output may contain bootstrap responses; do not print it on failure.
        raise RuntimeError(f"{Path(args[0]).name} {args[1]} failed with exit {result.returncode}")
    if not quiet and result.stdout:
        print(result.stdout.strip(), flush=True)
    return result.stdout


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubeconfig", required=True)
    parser.add_argument("--cluster", default="docker-install")
    parser.add_argument("--project", default="install-smoke", help="fresh application project; use a new name when replaying on an existing fixture")
    parser.add_argument("--control-image", required=True)
    parser.add_argument("--helmfile", default=shutil.which("helmfile"), required=not shutil.which("helmfile"))
    parser.add_argument("--interrupt", action="store_true", help="Interrupt during Infisical installation, then resume")
    parser.add_argument("--install-only", action="store_true", help="exercise installation without changing application test secrets")
    args = parser.parse_args()
    ops = dict(os.environ, KUBECONFIG=str(Path(args.kubeconfig).resolve()))
    config = json.loads(run(["kubectl", "config", "view", "--minify", "-o", "json"], ops, quiet=True))
    if config["clusters"][0]["cluster"]["server"] != "https://172.29.90.11:6443":
        parser.error("this test is restricted to the Docker fixture at 172.29.90.11")
    inspect = json.loads(run(["docker", "inspect", "k3s"], ops, quiet=True))[0]
    if inspect["Config"]["Image"] != "kuberpack-test/k3s:v1.37.1":
        parser.error("unexpected Docker fixture image")
    values = {}
    for line in (ROOT / "testenv/.env.test").read_text().splitlines():
        if "=" in line and not line.startswith("#"):
            name, value = line.removeprefix("export ").split("=", 1)
            parsed = shlex.split(value, comments=True)
            values[name] = parsed[0] if parsed else ""
    ops.update(KUBERPACK_GIT_TOKEN=values["FORGEJO_CONTROL_TOKEN"], KUBERPACK_REGISTRY_TOKEN=values["FORGEJO_REGISTRY_TOKEN"], KUBERPACK_BUILDER_GIT_TOKEN=values["FORGEJO_BUILDER_TOKEN"])
    workspace = Path(tempfile.mkdtemp(prefix="kuberpack-install-smoke-"))
    project_dir = workspace / "project"
    shutil.copytree(ROOT / "testenv/install", project_dir)
    cli = ROOT / "bin/kuberpack"

    def kubectl(*command):
        return json.loads(run(["kubectl", *command, "-o", "json"], ops, quiet=True))

    def fingerprint(namespaces):
        result = {}
        for namespace in namespaces:
            for obj in kubectl("get", "secrets,deployments,configmaps,pvc", "-n", namespace)["items"]:
                if obj["kind"] == "Secret" and obj.get("type") == "helm.sh/release.v1" and namespace in ("kuberpack", "infisical", "kuberpack-build"):
                    continue
                identity = namespace + "/" + obj["kind"] + "/" + obj["metadata"]["name"]
                state = {"uid": obj["metadata"]["uid"], "spec": obj.get("spec"), "data": obj.get("data")}
                if obj["kind"] == "PersistentVolumeClaim":
                    # Binding fills volumeName after creation; that transition
                    # is expected and does not mean the PVC was recreated.
                    state["spec"] = {k: v for k, v in obj["spec"].items() if k != "volumeName"}
                result[identity] = hashlib.sha256(json.dumps(state, sort_keys=True).encode()).hexdigest()
        return result

    run([args.helmfile, "-f", project_dir / "helmfile.yaml", "sync"], ops, quiet=True)
    ca_exists = subprocess.run(["kubectl", "get", "secret", "kuberpack-fixture-ca", "-n", "external-cert-manager"], env=ops, capture_output=True)
    if ca_exists.returncode:
        run(["kubectl", "create", "secret", "tls", "kuberpack-fixture-ca", "-n", "external-cert-manager", "--cert=" + str(ROOT / "testenv/pki/ca.crt"), "--key=" + str(ROOT / "testenv/pki/ca.key")], ops, quiet=True)
    run(["kubectl", "apply", "--field-manager=install-test-fixture", "-f", "-"], ops, quiet=True, input=json.dumps({"apiVersion": "cert-manager.io/v1", "kind": "ClusterIssuer", "metadata": {"name": "install-test-ca"}, "spec": {"ca": {"secretName": "kuberpack-fixture-ca"}}}))
    run(["kubectl", "wait", "--for=condition=Ready", "clusterissuer/install-test-ca", "--timeout=60s"], ops, quiet=True)
    external_before = fingerprint(["helmfile-apps", "external-cert-manager"])
    helmfile_before = hashlib.sha256((project_dir / "helmfile.yaml").read_bytes()).hexdigest()
    print("Independent Helmfile app and cert-manager installed", flush=True)

    context = ssl.create_default_context(cafile=str(ROOT / "testenv/pki/ca.crt"))
    for repository in ("install-audit", "install-audit-apps"):
        request = urllib.request.Request(values["FORGEJO_URL"] + "/api/v1/user/repos", json.dumps({"name": repository, "private": True, "auto_init": True, "default_branch": "main"}).encode(), {"Content-Type": "application/json", "Authorization": "token " + values["FORGEJO_CONTROL_TOKEN"]})
        try:
            urllib.request.urlopen(request, context=context).close()
        except urllib.error.HTTPError as error:
            if error.code != 409:
                raise RuntimeError(f"test repository creation: HTTP {error.code}") from None

    install = [cli, "install", "--cluster", args.cluster, "--gitops-url", values["FORGEJO_URL"] + "/kuberpack/install-audit.git", "--gitops-apps-url", values["FORGEJO_URL"] + "/kuberpack/install-audit-apps.git", "--host", "kuberpack.172.29.90.11.nip.io", "--issuer", "install-test-ca", "--ca-file", ROOT / "testenv/pki/ca.crt", "--image", args.control_image, "--builder-image", "docker.io/kuberpack-test/builder:dev", "--buildkit-image", "docker.io/kuberpack-test/buildkit:dev", "--node-port", "30080", "--allow-private-build-egress", "--webhook-url", "http://172.29.90.11:30080/hooks/forgejo"]
    apps_repository = "install-audit-apps"
    if args.interrupt:
        with (workspace / "interrupted.log").open("w") as output:
            process = subprocess.Popen([str(x) for x in install], env=ops, stdout=output, stderr=subprocess.STDOUT)
            try:
                for _ in range(450):
                    if process.poll() is not None:
                        raise RuntimeError("installer stopped before the interruption checkpoint")
                    pending = subprocess.run(["kubectl", "get", "secrets", "-n", "infisical", "-l", "owner=helm", "-o", "json"], env=ops, capture_output=True)
                    if pending.returncode == 0 and any(x["metadata"]["labels"].get("status") == "pending-install" for x in json.loads(pending.stdout)["items"]):
                        process.send_signal(signal.SIGINT)
                        process.wait(timeout=30)
                        break
                    time.sleep(2)
                else:
                    raise RuntimeError("Infisical installation checkpoint not reached")
            finally:
                if process.poll() is None:
                    process.send_signal(signal.SIGINT)
                    process.wait(timeout=30)
        interrupted = fingerprint(["kuberpack", "infisical", "kuberpack-build"])
        print("Installation interrupted deliberately; resuming with existing credentials", flush=True)
    else:
        interrupted = {}
    run(install, ops)
    if args.install_only:
        print("Installation-only check passed", flush=True)
        return
    current = fingerprint(["kuberpack", "infisical", "kuberpack-build"])
    # Workloads and generated configuration may converge; credential identities must remain.
    for key, value in interrupted.items():
        if "/Secret/" in key or "/PersistentVolumeClaim/" in key:
            assert current.get(key) == value, f"credential or PVC changed during recovery: {key}"

    def secret(name, namespace):
        return {k: base64.b64decode(v).decode() for k, v in kubectl("get", "secret", name, "-n", namespace)["data"].items()}

    admin = secret("infisical-bootstrap-credentials", "infisical")
    bootstrap = secret("infisical-bootstrap-secret", "infisical")
    control = secret("kuberpack", "kuberpack")
    project_id = kubectl("get", "configmap", "kuberpack-installation", "-n", "kuberpack")["data"]["infisical.project"]
    developer = dict(ops, KUBECONFIG="/tmp/no-kubernetes-access", INFISICAL_DOMAIN="https://infisical.172.29.90.11.nip.io", KUBERPACK_API_URL="https://kuberpack.172.29.90.11.nip.io", KUBERPACK_API_TOKEN=control["KUBERPACK_API_TOKEN"], SSL_CERT_FILE=str(ROOT / "testenv/pki/ca-bundle-merged.crt"))
    for key in ("INFISICAL_TOKEN", "KUBERPACK_GIT_TOKEN", "KUBERPACK_REGISTRY_TOKEN", "FORGEJO_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"):
        developer.pop(key, None)
    profile = "kuberpack-" + args.cluster
    run(["infisical", "login", "--method", "user", "--email", admin["INFISICAL_ADMIN_EMAIL"], "--password", admin["INFISICAL_ADMIN_PASSWORD"], "--organization-id", bootstrap["organizationId"], "--save-as", profile, "--silent"], developer, quiet=True)
    value = secrets.token_hex(24)
    private = workspace / "private.env"
    private.write_text("INSTALL_SMOKE_VALUE=" + value + "\n")
    private.chmod(0o600)
    run(["infisical", "secrets", "set", "--file", private, "--projectId", project_id, "--env", "prod", "--profile", profile, "--silent"], developer, quiet=True)
    manifest = project_dir / "kuberpack.yaml"
    manifest.write_text('''project: PROJECT_NAME
services:
  echo:
    image: docker.io/library/busybox:1.37.0
    hostname: PROJECT_NAME.172.29.90.11.nip.io
    port: 8080
    healthcheck: /
    command:
      - sh
      - -c
      - mkdir -p /tmp/www; printf '%s' "$INSTALL_SMOKE_VALUE" | sha256sum | cut -d' ' -f1 > /tmp/www/index.html; exec httpd -f -p 8080 -h /tmp/www
    secrets: [INSTALL_SMOKE_VALUE]
policies:
  defaultDeny: true
  internet: false
'''.replace("PROJECT_NAME", args.project))
    run([cli, "project", "compile", "-f", manifest], developer)
    expected = hashlib.sha256(value.encode()).hexdigest()
    for _ in range(240):
        try:
            with urllib.request.urlopen("https://" + args.project + ".172.29.90.11.nip.io", timeout=10, context=context) as response:
                if response.status == 200 and response.read().decode().strip() == expected:
                    break
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(2)
    else:
        raise RuntimeError("secret-backed application did not become healthy")
    assert '"changed":false' in run([cli, "project", "compile", "-f", manifest], developer)
    before_reinstall = fingerprint(["kuberpack", "infisical", "kuberpack-build"])
    # Confirm install itself is idempotent in Git, in addition to app compile.
    def git_revision(repository):
        request = urllib.request.Request(values["FORGEJO_URL"] + "/api/v1/repos/kuberpack/" + repository + "/branches/main", headers={"Authorization": "token " + values["FORGEJO_CONTROL_TOKEN"]})
        with urllib.request.urlopen(request, context=context) as response:
            return json.load(response)["commit"]["id"]
    before_revision = (git_revision("install-audit"), git_revision(apps_repository))
    run(install, ops)
    assert (git_revision("install-audit"), git_revision(apps_repository)) == before_revision, "reinstall produced a GitOps change"
    after_reinstall = fingerprint(["kuberpack", "infisical", "kuberpack-build"])
    for key, value in before_reinstall.items():
        if "/Secret/" in key or "/PersistentVolumeClaim/" in key:
            assert after_reinstall.get(key) == value, f"credential or PVC changed on reinstall: {key}"
    assert fingerprint(["helmfile-apps", "external-cert-manager"]) == external_before, "external Helmfile resources changed"
    assert hashlib.sha256((project_dir / "helmfile.yaml").read_bytes()).hexdigest() == helmfile_before
    result = {"installation": "ready", "reinstall": "passed", "interruption_recovery": bool(args.interrupt), "human_secret_upload": "passed", "application_secret_digest": "matched", "compile_idempotence": "passed", "helmfile_coexistence": "passed"}
    (workspace / "result.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result), flush=True)
    print("Test artifacts:", workspace, flush=True)


if __name__ == "__main__":
    main()

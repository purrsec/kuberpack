#!/usr/bin/env python3
"""Validate Kuberpack security on the disposable Docker fixture only."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

from _cli_v2 import FixtureAPI


ROOT = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubeconfig", required=True)
    args = parser.parse_args()
    ops = dict(os.environ, KUBECONFIG=str(Path(args.kubeconfig).resolve()))

    def run(command, *, env=ops, data=None, check=True, timeout=60):
        result = subprocess.run([str(x) for x in command], env=env, input=data, text=True, capture_output=True, timeout=timeout)
        if check and result.returncode:
            detail = (result.stderr or result.stdout).strip().splitlines()
            reason = f": {detail[-1][:300]}" if detail else ""
            raise RuntimeError(f"{Path(command[0]).name} {command[1]} failed (exit {result.returncode}){reason}")
        return result

    def kube(*command, check=True, data=None, timeout=60):
        return run(["kubectl", *command], check=check, data=data, timeout=timeout)

    config = json.loads(kube("config", "view", "--minify", "-o", "json").stdout)
    if config["clusters"][0]["cluster"]["server"] != "https://172.29.90.11:6443":
        parser.error("only the disposable Docker fixture at 172.29.90.11 is allowed")
    inspect = json.loads(run(["docker", "inspect", "k3s"]).stdout)[0]
    if inspect["Config"]["Image"] != "kuberpack-test/k3s:v1.37.1":
        parser.error("unexpected Docker fixture image")
    deployment = json.loads(kube("get", "deployment", "kuberpack", "-n", "kuberpack", "-o", "json").stdout)
    environment = {value["name"]: value.get("value", "") for value in deployment["spec"]["template"]["spec"]["containers"][0].get("env", [])}
    if environment.get("KUBERPACK_TENANT_ISOLATION") != "true":
        parser.error("run the installation fixture first; this security check requires tenant isolation")
    control = json.loads(kube("get", "secret", "kuberpack", "-n", "kuberpack", "-o", "json").stdout)
    import base64
    developer = dict(ops, KUBECONFIG="/tmp/no-security-developer-kubeconfig", KUBERPACK_API_URL="https://kuberpack.172.29.90.11.nip.io", KUBERPACK_API_TOKEN=base64.b64decode(control["data"]["KUBERPACK_API_TOKEN"]).decode())
    for key in ("KUBERPACK_GIT_TOKEN", "KUBERPACK_BUILDER_GIT_TOKEN", "KUBERPACK_REGISTRY_TOKEN", "FORGEJO_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"):
        developer.pop(key, None)
    workspace = Path(tempfile.mkdtemp(prefix="kuberpack-security-smoke-"))
    suffix = str(int(time.time()))[-7:]
    project = "security-a-" + suffix
    foreign = "security-b-" + suffix
    cli = ROOT / "bin/kuberpack"
    fixture = FixtureAPI(developer["KUBERPACK_API_URL"], developer["KUBERPACK_API_TOKEN"], ROOT / "testenv/pki/ca-bundle-merged.crt")
    administrator = dict(developer, SSL_CERT_FILE=str(ROOT / "testenv/pki/ca-bundle-merged.crt"))
    administrator_cli = ROOT / "bin/kuberpack-admin"

    created = []
    checks = {}

    def manifest(name, services):
        # JSON is also strict YAML, avoiding hand-assembled quoting of commands.
        source = {"project": name, "services": services, "policies": {"internet": False}}
        path = workspace / (name + ".yaml")
        path.write_text(json.dumps(source, indent=2) + "\n")
        return path

    def service(name, port, peers):
        command = "mkdir -p /tmp/www; printf '%s' security-ok > /tmp/www/index.html; "
        if name == "back":
            command += "httpd -f -p 9091 -h /tmp/www & "
        command += f"exec httpd -f -p {port} -h /tmp/www"
        return {"image": "docker.io/library/busybox:1.37.0", "hostname": f"{name}.{suffix}.172.29.90.11.nip.io", "port": port, "command": ["sh", "-c", command], "peers": peers}

    def compile_project(name, services):
        path = manifest(name, services)
        created.append(name)
        scoped = fixture.developer_environment(developer, name, {
            "domains": [value["hostname"] for value in services.values()], "repositories": []})
        run([cli, "deploy", "--file", path, "--yes", "--non-interactive", "--detach"], env=scoped, timeout=120)
        kube("wait", "--for=create", "namespace/" + name, "--timeout=240s", timeout=250)
        for service_name in services:
            kube("wait", "-n", name, "--for=create", "helmrelease/" + service_name, "--timeout=240s", timeout=250)
            kube("wait", "-n", name, "--for=condition=Ready", "helmrelease/" + service_name, "--timeout=240s", timeout=250)
        for service_name in services:
            kube("rollout", "status", "deployment/" + service_name, "-n", name, "--timeout=120s", timeout=130)

    def pod(namespace, name):
        objects = json.loads(kube("get", "pods", "-n", namespace, "-l", "app.kubernetes.io/name=" + name, "-o", "json").stdout)["items"]
        return next(p["metadata"]["name"] for p in objects if p.get("status", {}).get("phase") == "Running" and not p["metadata"].get("deletionTimestamp"))

    def probe(label, source_namespace, source_pod, destination, allowed):
        result = kube("exec", "-n", source_namespace, source_pod, "--", "wget", "-q", "-T", "3", "-O", "-", destination, check=False, timeout=12)
        success = result.returncode == 0 and result.stdout == "security-ok"
        if success != allowed:
            raise AssertionError(f"{label}: expected {'allowed' if allowed else 'denied'}, exit={result.returncode}")
        checks[label] = "allowed" if allowed else "denied"
        print(label + ": " + checks[label], flush=True)

    try:
        compile_project(project, {"front": service("front", 8080, ["back"]), "back": service("back", 8081, []), "other": service("other", 8080, [])})
        # Same service names, different project: selectors must not cross namespaces.
        other_service = service("back", 8081, [])
        other_service["hostname"] = f"foreign.{suffix}.172.29.90.11.nip.io"
        compile_project(foreign, {"back": other_service})
        front, back, other = pod(project, "front"), pod(project, "back"), pod(project, "other")
        back_ip = json.loads(kube("get", "pod", back, "-n", project, "-o", "json").stdout)["status"]["podIP"]
        probe("declared_service", project, front, "http://back:8081", True)
        probe("declared_pod", project, front, "http://" + back_ip + ":8081", True)
        probe("auxiliary_listener_exists", project, back, "http://127.0.0.1:9091", True)
        probe("undeclared_port", project, front, "http://" + back_ip + ":9091", False)
        probe("undeclared_neighbour", project, front, "http://other:8080", False)
        probe("reverse_direction", project, back, "http://front:8080", False)
        probe("other_service", project, other, "http://back:8081", False)
        probe("other_project", project, front, f"http://back.{foreign}.svc.cluster.local:8081", False)
        # An unselected helper has no egress policy: this detects an overly
        # broad TARGET ingress rule independently of source egress isolation.
        rogue = {"apiVersion":"v1", "kind":"Pod", "metadata":{"name":"unlabelled-probe", "namespace":project}, "spec":{"automountServiceAccountToken":False, "restartPolicy":"Never", "securityContext":{"runAsNonRoot":True,"runAsUser":65534,"seccompProfile":{"type":"RuntimeDefault"}}, "containers":[{"name":"probe", "image":"docker.io/library/busybox:1.37.0", "command":["sleep","600"], "securityContext":{"allowPrivilegeEscalation":False,"readOnlyRootFilesystem":True,"capabilities":{"drop":["ALL"]}}}]}}
        kube("apply", "-f", "-", data=json.dumps(rogue))
        kube("wait", "-n", project, "--for=condition=Ready", "pod/unlabelled-probe", "--timeout=90s", timeout=100)
        probe("target_ingress_without_source_egress_policy", project, "unlabelled-probe", "http://back:8081", False)
        dns = kube("exec", "-n", project, front, "--", "nslookup", "kubernetes.default.svc.cluster.local", check=False, timeout=12)
        assert dns.returncode == 0, "cluster DNS was blocked"
        checks["dns"] = "allowed"

        deployments = json.loads(kube("get", "deployments", "-n", "infisical", "-o", "json").stdout)["items"]
        operator = next(d for d in deployments if "controller-manager" in d["metadata"]["name"])
        operator_sa = operator["spec"]["template"]["spec"]["serviceAccountName"]
        web = next(d for d in deployments if "standalone-infisical" in d["metadata"]["name"])
        assert web["spec"]["template"]["spec"]["automountServiceAccountToken"] is False
        checks["infisical_web_token"] = "not mounted"
        for label, command, allowed in (
            ("operator_project_secrets", ["get","secrets","-n",project], True),
            ("operator_system_secrets", ["get","secrets","-n","kube-system"], False),
            ("operator_flux_token", ["create","serviceaccounts/token","-n","flux-system"], False),
            ("operator_control_secrets", ["get","secrets","-n","kuberpack"], False),
            ("control_exec", ["create","pods/exec","-n",project], False),
            ("control_cluster_secrets", ["get","secrets","-n","kube-system"], False),
        ):
            identity = "system:serviceaccount:infisical:" + operator_sa if label.startswith("operator_") else "system:serviceaccount:kuberpack:kuberpack"
            result = kube("auth","can-i",*command,"--as",identity, check=False)
            assert (result.returncode == 0) == allowed, label
            checks[label] = "allowed" if allowed else "denied"
        print(json.dumps(checks, sort_keys=True), flush=True)
    finally:
        # Remove only this test's foreign probe with the operator credential;
        # the product must keep refusing to prune foreign workloads.
        kube("delete","pod","unlabelled-probe","-n",project,"--ignore-not-found=true",check=False)
        for name in reversed(created):
            plan = json.loads(run([administrator_cli,"projects","destroy",name,"--plan","--output","json"],env=administrator,timeout=120).stdout)
            run([administrator_cli,"projects","destroy",name,"--plan-id",plan["id"],"--confirm-project",name,"--yes"],env=administrator,timeout=120)
        for name in created:
            # Product destruction preserves namespaces. The disposable-fixture
            # operator explicitly removes them using its own kubeconfig.
            kube("delete","namespace",name,"--wait=true","--timeout=120s",timeout=130)
    (workspace / "result.json").write_text(json.dumps(checks, indent=2) + "\n")
    print("Test artifacts:", workspace, flush=True)


if __name__ == "__main__":
    main()

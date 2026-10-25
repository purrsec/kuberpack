#!/usr/bin/env python3
"""Verify Stirling through HTTPS, using developer credentials and no kubeconfig."""

import argparse
import hashlib
import json
import re
import shlex
import shutil
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path


def one_page_pdf(text):
    stream = f"BT /F1 18 Tf 40 160 Td ({text}) Tj ET".encode()
    objects = [
        b"<< /Type /Catalog /Pages 2 0 R >>",
        b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
        b"<< /Length " + str(len(stream)).encode() + b" >>\nstream\n" + stream + b"\nendstream",
    ]
    result, offsets = b"%PDF-1.4\n", [0]
    for index, obj in enumerate(objects, 1):
        offsets.append(len(result))
        result += str(index).encode() + b" 0 obj\n" + obj + b"\nendobj\n"
    xref = len(result)
    result += b"xref\n0 6\n0000000000 65535 f \n"
    result += b"".join(f"{offset:010d} 00000 n \n".encode() for offset in offsets[1:])
    return result + b"trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n" + str(xref).encode() + b"\n%%EOF\n"


def multipart(field, files):
    boundary = "KuberpackAudit" + uuid.uuid4().hex
    body = b""
    for name, content in files:
        body += f'--{boundary}\r\nContent-Disposition: form-data; name="{field}"; filename="{name}"\r\nContent-Type: application/pdf\r\n\r\n'.encode()
        body += content + b"\r\n"
    body += f"--{boundary}--\r\n".encode()
    return body, {"Content-Type": "multipart/form-data; boundary=" + boundary}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", required=True)
    parser.add_argument("--secrets-file", required=True, type=Path)
    parser.add_argument("--state-file", type=Path, help="Upload a test PDF on the first run; verify it on later runs")
    parser.add_argument("--timeout", type=int, default=600)
    args = parser.parse_args()
    if not shutil.which("pdfinfo"):
        parser.error("pdfinfo is required (poppler-utils)")
    secrets = {}
    for line in args.secrets_file.read_text().splitlines():
        if "=" in line and not line.lstrip().startswith("#"):
            name, value = line.removeprefix("export ").split("=", 1)
            parts = shlex.split(value, comments=True)
            if len(parts) != 1:
                parser.error(f"{name.strip()} must contain one dotenv value; quote spaces")
            secrets[name.strip()] = parts[0]

    def request(path, body=None, headers=None):
        req = urllib.request.Request(args.url.rstrip("/") + path, data=body, headers=headers or {})
        try:
            with urllib.request.urlopen(req, timeout=45) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as error:
            return error.code, error.read()

    deadline = time.monotonic() + args.timeout
    while time.monotonic() < deadline:
        try:
            status, body = request("/api/v1/info/status")
            if status == 200:
                break
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(5)
    else:
        raise RuntimeError("Stirling did not become healthy within the startup budget")
    print("Health:", body.decode(), flush=True)
    status, body = request("/")
    assert status == 200 and b"<html" in body.lower(), "Web interface did not return HTML"

    login = json.dumps({"username": secrets["SECURITY_INITIALLOGIN_USERNAME"], "password": secrets["SECURITY_INITIALLOGIN_PASSWORD"]}).encode()
    status, body = request("/api/v1/auth/login", login, {"Content-Type": "application/json"})
    assert status == 200, f"Developer login returned HTTP {status}"
    auth = json.loads(body)
    print("Developer login: 200", flush=True)

    payload, headers = multipart("fileInput", [("first.pdf", one_page_pdf("First page")), ("second.pdf", one_page_pdf("Second page"))])
    status, _ = request("/api/v1/general/merge-pdfs", payload, headers)
    assert status in (401, 403), f"Unauthenticated PDF operation returned HTTP {status}"
    print("Unauthenticated operation:", status, flush=True)
    api_headers = {"X-API-KEY": secrets["SECURITY_CUSTOMGLOBALAPIKEY"]}
    status, merged = request("/api/v1/general/merge-pdfs", payload, dict(headers, **api_headers))
    assert status == 200 and merged.startswith(b"%PDF-"), f"Authenticated merge returned HTTP {status}"
    with tempfile.TemporaryDirectory(prefix="kuberpack-stirling-") as directory:
        output = Path(directory) / "merged.pdf"
        output.write_bytes(merged)
        result = subprocess.run(["pdfinfo", str(output)], capture_output=True, text=True, check=True)
        assert re.search(r"Pages:\s+2\b", result.stdout), "Merged PDF does not contain two pages"
    print("Authenticated merge: 200, two pages validated", flush=True)

    if args.state_file:
        token = auth.get("accessToken") or auth.get("token")
        storage_headers = {"Authorization": "Bearer " + token} if token else api_headers
        if args.state_file.exists():
            state = json.loads(args.state_file.read_text())
            status, content = request(f"/api/v1/storage/files/{state['id']}/download", headers=storage_headers)
            assert status == 200 and hashlib.sha256(content).hexdigest() == state["sha256"], "Stored PDF did not survive the rollout"
            print("Persistence: stored PDF checksum unchanged", flush=True)
        else:
            payload, headers = multipart("file", [("kuberpack-audit.pdf", merged)])
            status, body = request("/api/v1/storage/files", payload, dict(headers, **storage_headers))
            assert status == 200, f"PDF storage returned HTTP {status}"
            state = {"id": json.loads(body)["id"], "sha256": hashlib.sha256(merged).hexdigest()}
            args.state_file.write_text(json.dumps(state) + "\n")
            print("Persistence: test PDF uploaded; run again after a rollout", flush=True)


if __name__ == "__main__":
    main()

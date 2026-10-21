"""Minimal FastAPI application used to exercise Kuberpack end to end.

The application is deliberately tiny: what matters for the test environment is
the pipeline (webhook -> build -> digest pin -> Flux -> running pod), not the
application itself.

It exposes:
  GET /         -> human readable banner
  GET /healthz  -> probe target used by the Kubernetes probes and the Helm test
"""

from __future__ import annotations

import os
import socket

from fastapi import FastAPI

APP_VERSION = "0.4.0"

app = FastAPI(title="hello-kuberpack", version=APP_VERSION)


@app.get("/")
def index() -> dict[str, str]:
    return {
        "app": "hello-kuberpack",
        "version": APP_VERSION,
        "message": "Deployed by Kuberpack (Railpack build, digest pin, Flux reconcile).",
        "hostname": socket.gethostname(),
    }


@app.get("/healthz")
def healthz() -> dict[str, str]:
    return {"status": "ok", "port": os.environ.get("PORT", "unknown")}

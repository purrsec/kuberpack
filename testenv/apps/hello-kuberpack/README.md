# hello-kuberpack

Test application for the Kuberpack end-to-end environment.

It is a plain FastAPI service with a `uv` lockfile, so the builder runs the
`uv` strategy (Railpack as the executor, `uv.lock` consumed by the frontend).

```bash
uv lock          # refresh uv.lock
uv run uvicorn main:app --host 0.0.0.0 --port 8080
```

The Kubernetes probes and the Helm test target `/healthz`.

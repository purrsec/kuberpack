FROM python:3.15.0-slim-bookworm@sha256:0e019bbdfb5d9874cb2389f1c2884b64aa2ac97c99960b5bc521cef978c0617d
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates poppler-utils \
    && rm -rf /var/lib/apt/lists/*
COPY stirling.py /opt/stirling.py
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
ENTRYPOINT ["python3", "/opt/stirling.py"]

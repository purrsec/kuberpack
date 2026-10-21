# Forgejo: Git server (Gitea API) + OCI registry for the Kuberpack test env.
# Base image is a plain Debian, as required for a realistic test setup.
FROM debian:bookworm-slim

ARG FORGEJO_VERSION=17.0.0
ARG DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl git git-lfs openssh-server python3 python3-venv \
        sqlite3 tzdata \
    && rm -rf /var/lib/apt/lists/*

# Forgejo refuses to run as root: create the `git` account (uid 1000).
RUN useradd --create-home --uid 1000 --shell /bin/bash git \
    && mkdir -p /var/lib/forgejo /etc/forgejo \
    && chown -R git:git /var/lib/forgejo /etc/forgejo

RUN curl -fsSL -o /tmp/forgejo \
        "https://codeberg.org/forgejo/forgejo/releases/download/v${FORGEJO_VERSION}/forgejo-${FORGEJO_VERSION}-linux-amd64" \
    && install -m 0755 /tmp/forgejo /usr/local/bin/forgejo \
    && rm -f /tmp/forgejo \
    && forgejo --version

# Trust the local test CA (the self-signed registry/Git certificate).
COPY pki/ca.crt /usr/local/share/ca-certificates/kuberpack-test-ca.crt
RUN update-ca-certificates

COPY images/forgejo/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh

USER git
WORKDIR /var/lib/forgejo
EXPOSE 3000 22
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

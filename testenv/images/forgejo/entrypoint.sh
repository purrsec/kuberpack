#!/usr/bin/env bash
# Entrypoint for the test Forgejo instance.
#
# Everything is driven by environment variables so the same image can be
# re-created for another test run. The TLS material is mounted read-only.
set -euo pipefail

FORGEJO_HOME="${FORGEJO_HOME:-/var/lib/forgejo}"
APP_INI="${APP_INI:-/etc/forgejo/app.ini}"
TLS_DIR="${TLS_DIR:-/etc/forgejo/tls}"
SSH_HOST_KEY="${SSH_HOST_KEY:-/var/lib/forgejo/ssh/ssh_host_ed25519_key}"

mkdir -p "${FORGEJO_HOME}/data" "${FORGEJO_HOME}/log" "${FORGEJO_HOME}/ssh" "${TLS_DIR}"

# --- SSH host key -----------------------------------------------------------
if [ ! -f "${SSH_HOST_KEY}" ]; then
    ssh-keygen -q -t ed25519 -N '' -f "${SSH_HOST_KEY}"
    cp "${SSH_HOST_KEY}.pub" "${SSH_HOST_KEY}.pub"
fi
chmod 0600 "${SSH_HOST_KEY}"

# --- app.ini ----------------------------------------------------------------
# The configuration is regenerated on every start: the test environment is
# driven by environment variables, not by hand-edited files.
{
    cat <<EOF
APP_NAME = ${FORGEJO_APP_NAME:-Kuberpack Test Forgejo}
RUN_USER = git
RUN_MODE = prod
WORK_PATH = ${FORGEJO_HOME}

[server]
PROTOCOL = https
DOMAIN = ${FORGEJO_DOMAIN}
HTTP_ADDR = 0.0.0.0
HTTP_PORT = 3000
ROOT_URL = ${FORGEJO_ROOT_URL}/
CERT_FILE = ${TLS_DIR}/forgejo.crt
KEY_FILE = ${TLS_DIR}/forgejo.key
DISABLE_SSH = false
SSH_DOMAIN = ${FORGEJO_DOMAIN}
SSH_PORT = ${FORGEJO_SSH_PORT:-2222}
SSH_LISTEN_PORT = 22
SSH_ROOT_PATH = ${FORGEJO_HOME}/ssh
START_SSH_SERVER = false
LFS_START_SERVER = true
OFFLINE_MODE = true
ENABLE_GZIP = true
APP_DATA_URL = ${FORGEJO_ROOT_URL}/

[database]
DB_TYPE = sqlite3
PATH = ${FORGEJO_HOME}/data/forgejo.db
SQLITE_TIMEOUT = 500

[repository]
ROOT = ${FORGEJO_HOME}/data/git/repositories
DEFAULT_BRANCH = main
DEFAULT_PRIVATE = public

[repository.oci]
ENABLED = true

[repository.upload]
ENABLED = true
FILE_MAX_SIZE = 100
MAX_FILES = 10

[security]
INSTALL_LOCK = true
SECRET_KEY = ${FORGEJO_SECRET_KEY}
INTERNAL_TOKEN = ${FORGEJO_INTERNAL_TOKEN}

[service]
DISABLE_REGISTRATION = true
REQUIRE_SIGNIN_VIEW = false
DEFAULT_KEEP_EMAIL_PRIVATE = true

[webhook]
ALLOWED_HOST_LIST = ${FORGEJO_ALLOWED_HOST_LIST:-loopback, private}
DELIVER_TIMEOUT = 30

[actions]
ENABLED = false

[log]
MODE = console
LEVEL = info
ROOT_PATH = ${FORGEJO_HOME}/log

[session]
PROVIDER = file
PROVIDER_CONFIG = ${FORGEJO_HOME}/data/sessions

[picture]
DISABLE_GRAVATAR = true
EOF
} >"${APP_INI}.tmp"
mv "${APP_INI}.tmp" "${APP_INI}"

# Forgejo refuses to start with a world-readable app.ini.
chmod 0600 "${APP_INI}"
chown git:git "${APP_INI}" 2>/dev/null || true
# The data directory may be a mounted volume owned by root: the files
# themselves already belong to the git user, so a failure here is not fatal.
chown -R git:git "${FORGEJO_HOME}" 2>/dev/null || true

echo "forgejo: starting web server (root url ${FORGEJO_ROOT_URL})"
exec forgejo web --config "${APP_INI}"

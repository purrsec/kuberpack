#!/usr/bin/env bash
# Push the test application to the Forgejo repository.
#
# Usage: push-app.sh [message]
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TESTENV="$(dirname "${HERE}")"

# shellcheck disable=SC1091
set -a; . "${TESTENV}/.env.test"; set +a
export GIT_SSL_CAINFO="${TESTENV}/pki/ca-bundle-merged.crt"

MESSAGE="${1:-app: update hello-kuberpack}"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

git clone --quiet \
    "https://kuberpack:${FORGEJO_CONTROL_TOKEN}@${FORGEJO_HOST}:${FORGEJO_PORT}/${APP_REPO}.git" "${WORK}/app"

cd "${WORK}/app"
git config user.email "kuberpack@test.local"
git config user.name "Kuberpack Test"

# Replace the working tree by the local source of the application.
find . -mindepth 1 -maxdepth 1 -not -name '.git' -exec rm -rf {} +
cp -a "${TESTENV}/apps/hello-kuberpack/." .

git add -A
if git diff --cached --quiet; then
    echo "nothing to commit"
else
    git commit -q -m "${MESSAGE}"
    git push -q origin HEAD:main
    echo "pushed $(git rev-parse HEAD) to ${APP_REPO}"
fi

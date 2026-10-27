#!/usr/bin/env bash
# One-shot deploy: unpack dbx source -> build its image if missing -> build void2md and start all services
set -euo pipefail
cd "$(dirname "$0")"

DBX_ZIP="dbx-0.6.11.zip"
DBX_DIR="dbx"
DBX_SRC="${DBX_DIR}/dbx-0.6.11"
DBX_IMAGE="t8y2/dbx:latest"

# 1. Unpack dbx source (skip if already unpacked)
if [ ! -d "${DBX_SRC}" ]; then
    echo ">> Unpacking ${DBX_ZIP} -> ${DBX_DIR}/"
    unzip -q "${DBX_ZIP}" -d "${DBX_DIR}"
fi

# 2. Build the dbx image from source when missing (first build is slow; cached afterwards)
if ! docker image inspect "${DBX_IMAGE}" >/dev/null 2>&1; then
    echo ">> ${DBX_IMAGE} not found locally, building from source..."
    docker build -f "${DBX_SRC}/deploy/Dockerfile" -t "${DBX_IMAGE}" "${DBX_SRC}"
else
    echo ">> ${DBX_IMAGE} exists, skipping build"
fi

# 3. Build void2md and start all services (BuildKit incremental cache)
export DOCKER_BUILDKIT=1 COMPOSE_DOCKER_CLI_BUILD=1
docker compose up -d --build

docker compose ps
echo ">> Deploy done: void2md(:3457)  dbx(:4225)"

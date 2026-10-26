#!/usr/bin/env bash
# 一键部署：解压 dbx 源码 -> 缺镜像时从源码构建 -> 构建 void2md 并拉起全部服务
set -euo pipefail
cd "$(dirname "$0")"

DBX_ZIP="dbx-0.6.11.zip"
DBX_DIR="dbx"
DBX_SRC="${DBX_DIR}/dbx-0.6.11"
DBX_IMAGE="t8y2/dbx:latest"

# 1. 解压 dbx 源码（已解压则跳过）
if [ ! -d "${DBX_SRC}" ]; then
    echo ">> 解压 ${DBX_ZIP} -> ${DBX_DIR}/"
    unzip -q "${DBX_ZIP}" -d "${DBX_DIR}"
fi

# 2. dbx 镜像不存在时，从源码构建（首次构建耗时较长，之后走缓存）
if ! docker image inspect "${DBX_IMAGE}" >/dev/null 2>&1; then
    echo ">> 本地无 ${DBX_IMAGE}，从源码构建..."
    docker build -f "${DBX_SRC}/deploy/Dockerfile" -t "${DBX_IMAGE}" "${DBX_SRC}"
else
    echo ">> ${DBX_IMAGE} 已存在，跳过构建"
fi

# 3. 构建 void2md 并拉起全部服务（BuildKit 增量缓存，快速构建）
export DOCKER_BUILDKIT=1 COMPOSE_DOCKER_CLI_BUILD=1
docker compose up -d --build

docker compose ps
echo ">> 部署完成：void2md(:3457)  dbx(:4225)"

# Multi-stage Docker build for Go Gin + Rust void2md service
#
# China-friendly build: apt / crates.io / Go module downloads all go through
# domestic mirrors. Override the apt mirror if aliyun is slow for you:
#   docker build --build-arg APT_MIRROR=mirrors.ustc.edu.cn -t void2md .

ARG APT_MIRROR=mirrors.aliyun.com
# Base images are overridable so a mirror/proxy can be used when Docker Hub
# pulls are slow, e.g.:
#   docker build --build-arg RUST_IMAGE=docker.m.daocloud.io/library/rust:1.93 .
ARG RUST_IMAGE=rust:1.93
ARG GO_IMAGE=golang:1.26.10
ARG RUNTIME_IMAGE=debian:stable-slim

# Stage 1: Build Rust library
FROM ${RUST_IMAGE} AS rust-builder
ARG APT_MIRROR
ENV DEBIAN_FRONTEND=noninteractive

# Point apt at a domestic mirror (handles both sources.list and deb822 .sources)
RUN set -eux; \
    for f in /etc/apt/sources.list /etc/apt/sources.list.d/debian.sources; do \
        [ -f "$f" ] || continue; \
        sed -i \
            -e "s|deb.debian.org|${APT_MIRROR}|g" \
            -e "s|security.debian.org|${APT_MIRROR}|g" \
            "$f"; \
    done; \
    apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Use the rsproxy.cn crates.io mirror (sparse index, fastest option in CN)
RUN mkdir -p "${CARGO_HOME:-/usr/local/cargo}" && \
    printf '%s\n' \
        '[source.crates-io]' \
        'replace-with = "rsproxy-sparse"' \
        '' \
        '[source.rsproxy-sparse]' \
        'registry = "sparse+https://rsproxy.cn/index/"' \
        '' \
        '[net]' \
        'git-fetch-with-cli = true' \
        > "${CARGO_HOME:-/usr/local/cargo}/config.toml"

WORKDIR /app

# Copy Rust source and build the shared library.
# registry/ and target/ are cache mounts: crates are downloaded once from the
# mirror and compiled dependencies are reused on the next build. The final .so
# is copied out into the image layer where later stages can grab it.
COPY file/rust/ ./rust/

RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    --mount=type=cache,target=/app/rust/target \
    cd rust && cargo build --release --locked && \
    mkdir -p /app/out && cp -f target/release/libvoid2md.so /app/out/

# Stage 2: Build Go application with vendored dependencies
FROM ${GO_IMAGE} AS go-builder
ARG APT_MIRROR
ENV DEBIAN_FRONTEND=noninteractive

# Install CGO dependencies via a domestic apt mirror
RUN set -eux; \
    for f in /etc/apt/sources.list /etc/apt/sources.list.d/debian.sources; do \
        [ -f "$f" ] || continue; \
        sed -i \
            -e "s|deb.debian.org|${APT_MIRROR}|g" \
            -e "s|security.debian.org|${APT_MIRROR}|g" \
            "$f"; \
    done; \
    apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        gcc \
    && rm -rf /var/lib/apt/lists/*

# Domestic Go module proxy / checksum db (put before any go command runs)
ENV GOPROXY=https://goproxy.cn,https://goproxy.io,direct
ENV GOSUMDB=sum.golang.google.cn
ENV GOTOOLCHAIN=local

WORKDIR /app

# Copy Go mod files first (for better layer caching) and download deps once
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# Copy all source code
COPY . .

# Copy Rust shared library from stage 1
COPY --from=rust-builder /app/out/libvoid2md.so /usr/lib/libvoid2md.so

# Set CGO flags for Rust integration
ENV CGO_ENABLED=1
ENV CGO_LDFLAGS="-L/usr/lib -lvoid2md"
ENV LD_LIBRARY_PATH="/usr/lib"

# Build Go binary
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -buildvcs=false -o void2md-api .

# Stage 3: Runtime
FROM ${RUNTIME_IMAGE} AS runtime
ARG APT_MIRROR
# DuckDB extension architecture (set automatically by BuildKit, e.g. arm64/amd64).
ARG TARGETARCH
ENV DEBIAN_FRONTEND=noninteractive

# Install CA certificates via a domestic apt mirror and copy shared library
RUN set -eux; \
    for f in /etc/apt/sources.list /etc/apt/sources.list.d/debian.sources; do \
        [ -f "$f" ] || continue; \
        sed -i \
            -e "s|deb.debian.org|${APT_MIRROR}|g" \
            -e "s|security.debian.org|${APT_MIRROR}|g" \
            "$f"; \
    done; \
    apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /app

# Copy shared library and data directory to /app (matching file-parse service structure)
COPY --from=rust-builder /app/out/libvoid2md.so /usr/lib/libvoid2md.so
COPY file/data/ /app/data/

# Copy dbc config files (dbc API is mounted under /dbc, config path via DBC_CONFIG)
COPY dbc/config/ /app/dbc/config/
ENV DBC_CONFIG=/app/dbc/config/config.json

# DuckDB extensions bundled for offline LOAD (the runtime image has no network
# guarantee, and the structured S3 store loads excel + httpfs on every connect).
# Directory layout matches DuckDB's default extension search path.
COPY structured/duckdb_extensions/${TARGETARCH}/ /root/.duckdb/extensions/v2.0.0/linux_${TARGETARCH}/

# Copy Go binary
COPY --from=go-builder /app/void2md-api /app/void2md-api

# Set permissions and refresh the loader cache for /usr/lib/libvoid2md.so
RUN chmod +x /app/void2md-api && ldconfig

# Expose port
EXPOSE 3457

# Set working directory and entrypoint
WORKDIR /app
ENTRYPOINT ["./void2md-api"]
# Multi-stage Docker build for Go Gin + Rust void2md service

# Stage 1: Build Rust library
FROM rust:1.93 AS rust-builder

# Install dependencies
RUN apt-get update && apt-get install -y \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Create app directory
WORKDIR /app

# Copy Rust source and data directory
COPY file/rust/ ./rust/
COPY file/data/ ./data/

# Build Rust library as shared library
RUN cd rust && \
    cargo build --release

# Stage 2: Build Go application with vendored dependencies
FROM golang:1.26.10 AS go-builder

# Install CGO dependencies
RUN apt-get update && apt-get install -y \
    ca-certificates \
    gcc \
    && rm -rf /var/lib/apt/lists/*

# Create app directory
WORKDIR /app

# Copy Go mod files first (for better layer caching)
COPY go.mod ./
COPY go.sum ./

# Download dependencies to separate layer (uncomment if you have go.sum)
# RUN go mod download

# Copy all source code including data directory (force rebuild by adding timestamp)
COPY . .

# Copy Rust shared library from stage 1
COPY --from=rust-builder /app/rust/target/release/libvoid2md.so /usr/lib/libvoid2md.so

# Set CGO flags for Rust integration
ENV CGO_ENABLED=1
ENV CGO_LDFLAGS="-L/usr/lib -lvoid2md"
ENV LD_LIBRARY_PATH="/usr/lib:${LD_LIBRARY_PATH}"

# Optional: Uncomment the following lines for faster downloads in China
ENV GOPROXY=https://goproxy.cn,direct
ENV GOSUMDB=sum.golang.google.cn
# For global builds, leave these commented out to use default Go proxies

# Build Go binary - try with go mod tidy first if go.sum doesn't exist
RUN if [ ! -f go.sum ]; then \
        echo "go.sum not found, attempting to generate..."; \
        go mod tidy; \
    fi && \
    go build -o void2md-api .

# Stage 3: Runtime
FROM debian:stable-slim

# Install CA certificates and copy shared library
RUN apt-get update && apt-get install -y \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/* && \
    mkdir -p /app

# Copy shared library and data directory to /app (matching file-parse service structure)
COPY --from=rust-builder /app/rust/target/release/libvoid2md.so /usr/lib/libvoid2md.so
COPY --from=rust-builder /app/data/ /app/data/

# Copy dbc config files (dbc API is mounted under /dbc, config path via DBC_CONFIG)
COPY dbc/config/ /app/dbc/config/
ENV DBC_CONFIG=/app/dbc/config/config.json

# Copy Go binary
COPY --from=go-builder /app/void2md-api /app/void2md-api

# Set permissions
RUN chmod +x /app/void2md-api

# Expose port
EXPOSE 3457

# Set working directory and entrypoint
WORKDIR /app
ENTRYPOINT ["./void2md-api"]
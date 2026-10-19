# ----------------------
# Build stage: compile Go application
# ----------------------
FROM golang:1.24 AS builder

WORKDIR /app

# 安装构建依赖 (libopus 开发包 + pkg-config)
RUN apt-get update && apt-get install -y --no-install-recommends \
    pkg-config \
    libopus-dev \
 && rm -rf /var/lib/apt/lists/*

# Copy dependency files first for better caching
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy the rest of the application
COPY . .

# Build the Go application, reusing the module and build caches across builds (needs BuildKit)
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags="-s -w" -o MuseBot main.go && \
    go build -ldflags="-s -w" -o MuseBotAdmin admin/main.go


# ----------------------
# Runtime stage
# ----------------------
FROM debian:stable-slim

WORKDIR /app

# 安装运行时依赖 (证书 + nodejs + opus 运行库)
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates curl libopus0 supervisor && \
    curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
    apt-get install -y --no-install-recommends nodejs && \
    rm -rf /var/lib/apt/lists/*

ENV PATH="/usr/share/nodejs/corepack/shims:$PATH"

COPY supervisord.conf /etc/supervisor/conf.d/supervisord.conf

# Create required directories
RUN mkdir -p ./conf/i18n ./conf/mcp ./conf/img/ ./adminui

# Copy compiled Go application
COPY --from=builder /app/MuseBot .
COPY --from=builder /app/MuseBotAdmin .
COPY --from=builder /app/conf/i18n/ ./conf/i18n/
COPY --from=builder /app/conf/mcp/ ./conf/mcp/
COPY --from=builder /app/admin/adminui/ ./conf/adminui/

# Copy static FFmpeg binaries (multi-arch image, no download from a single mirror)
COPY --from=mwader/static-ffmpeg:7.1 /ffmpeg /usr/local/bin/ffmpeg
COPY --from=mwader/static-ffmpeg:7.1 /ffprobe /usr/local/bin/ffprobe

# Create non-root user for security
RUN useradd -m appuser && \
    chown -R appuser:appuser /app
USER appuser

# Expose application port
EXPOSE 36060
EXPOSE 18080

# Set entrypoint
ENTRYPOINT ["/usr/bin/supervisord", "-c", "/etc/supervisor/conf.d/supervisord.conf", "-n"]

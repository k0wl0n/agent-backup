# Build Stage
FROM golang:1.25.3-bookworm AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG BUILD_TIME=unknown
ARG GIT_COMMIT=unknown
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build \
    -ldflags="-X github.com/k0wl0n/agent-backup/internal/version.Version=${VERSION} \
              -X github.com/k0wl0n/agent-backup/internal/version.BuildTime=${BUILD_TIME} \
              -X github.com/k0wl0n/agent-backup/internal/version.GitCommit=${GIT_COMMIT}" \
    -o /app/jokowipe-agent ./cmd/agent

# Runtime Stage
FROM debian:bookworm-slim

ARG TARGETARCH

# Install base tools
RUN rm -rf /var/cache/apt/archives/*.deb /var/lib/apt/lists/* \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
       ca-certificates \
       gnupg \
       curl \
       wget \
    && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb

# Add PostgreSQL repository
RUN curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc \
       | gpg --dearmor -o /usr/share/keyrings/postgresql.gpg \
    && echo "deb [ signed-by=/usr/share/keyrings/postgresql.gpg ] https://apt.postgresql.org/pub/repos/apt bookworm-pgdg main" \
       | tee /etc/apt/sources.list.d/pgdg.list

# Install database clients
RUN rm -rf /var/cache/apt/archives/*.deb /var/lib/apt/lists/* \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
       postgresql-client-16 \
       mariadb-client \
       redis-tools \
       zip \
    && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb

# Install MongoDB tools (architecture-specific) - DISABLED for now due to ARM64 build issues
# RUN if [ "$TARGETARCH" = "amd64" ]; then \
#          curl -fsSL https://www.mongodb.org/static/pgp/server-7.0.asc \
#            | gpg --dearmor -o /usr/share/keyrings/mongodb-server-7.0.gpg \
#          && echo "deb [ arch=amd64 signed-by=/usr/share/keyrings/mongodb-server-7.0.gpg ] https://repo.mongodb.org/apt/debian bookworm/mongodb-org/7.0 main" \
#            | tee /etc/apt/sources.list.d/mongodb-org-7.0.list \
#          && rm -rf /var/cache/apt/archives/*.deb /var/lib/apt/lists/* \
#          && apt-get update \
#          && apt-get install -y --no-install-recommends mongodb-database-tools \
#          && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb; \
#        elif [ "$TARGETARCH" = "arm64" ]; then \
#          MONGO_TOOLS_VERSION=100.9.4 \
#          && wget -q "https://fastdl.mongodb.org/tools/db/mongodb-database-tools-debian12-arm64-${MONGO_TOOLS_VERSION}.deb" \
#          && dpkg -i "mongodb-database-tools-debian12-arm64-${MONGO_TOOLS_VERSION}.deb" \
#          && rm "mongodb-database-tools-debian12-arm64-${MONGO_TOOLS_VERSION}.deb"; \
#        fi

# Cleanup
RUN apt-get purge -y gnupg curl wget \
    && apt-get autoremove -y \
    && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb

RUN groupadd -r appgroup && useradd -r -g appgroup -m -d /home/appuser appuser

RUN mkdir -p /etc/jokowipe /var/lib/jokowipe/backups && \
    chown -R appuser:appgroup /etc/jokowipe /var/lib/jokowipe /home/appuser

ENV JOKOWIPE_GATEWAY_ENABLED=true

COPY --from=builder /app/jokowipe-agent /usr/local/bin/jokowipe-agent
RUN chmod +x /usr/local/bin/jokowipe-agent

USER appuser

ENTRYPOINT ["/usr/local/bin/jokowipe-agent"]
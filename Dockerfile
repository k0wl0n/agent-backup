# Build Stage
FROM golang:1.25.3-bookworm AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build arguments for version information
ARG VERSION=dev
ARG BUILD_TIME=unknown
ARG GIT_COMMIT=unknown

# Build for Linux AMD64 by default with version information injected via ldflags
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-X github.com/k0wl0n/agent-backup/internal/version.Version=${VERSION} \
              -X github.com/k0wl0n/agent-backup/internal/version.BuildTime=${BUILD_TIME} \
              -X github.com/k0wl0n/agent-backup/internal/version.GitCommit=${GIT_COMMIT}" \
    -o /app/jokowipe-agent ./cmd/agent

# Runtime Stage
FROM debian:bookworm-slim

# Install backup tool dependencies:
#   postgresql-client-16 → pg_dump v16 (from PGDG; matches server version 16.x)
#   mariadb-client       → mysqldump (MySQL, MariaDB)
#   redis-tools          → redis-cli --rdb (Redis)
#   gnupg / curl         → required to add PGDG and MongoDB APT repos
# Then install MongoDB Database Tools (mongodump) from official repo.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    gnupg \
    curl \
    && curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc \
       | gpg --dearmor -o /usr/share/keyrings/postgresql.gpg \
    && echo "deb [ signed-by=/usr/share/keyrings/postgresql.gpg ] https://apt.postgresql.org/pub/repos/apt bookworm-pgdg main" \
       | tee /etc/apt/sources.list.d/pgdg.list \
    && curl -fsSL https://www.mongodb.org/static/pgp/server-7.0.asc \
       | gpg --dearmor -o /usr/share/keyrings/mongodb-server-7.0.gpg \
    && echo "deb [ arch=amd64,arm64 signed-by=/usr/share/keyrings/mongodb-server-7.0.gpg ] https://repo.mongodb.org/apt/debian bookworm/mongodb-org/7.0 main" \
       | tee /etc/apt/sources.list.d/mongodb-org-7.0.list \
    && apt-get update && apt-get install -y --no-install-recommends \
    postgresql-client-16 \
    mariadb-client \
    redis-tools \
    mongodb-database-tools \
    zip \
    && apt-get purge -y gnupg curl \
    && apt-get autoremove -y \
    && rm -rf /var/lib/apt/lists/*

# Create user with home directory so config mounts work cleanly
RUN groupadd -r appgroup && useradd -r -g appgroup -m -d /home/appuser appuser

# Create directory for config and backups and set permissions
RUN mkdir -p /etc/jokowipe /var/lib/jokowipe/backups && \
    chown -R appuser:appgroup /etc/jokowipe /var/lib/jokowipe /home/appuser

# Default environment — works with plain `docker run -e JOKOWIPE_API_KEY=...`
# Gateway must be enabled so the agent polls the platform for backup tasks.
ENV JOKOWIPE_GATEWAY_ENABLED=true

# Copy binary from builder
COPY --from=builder /app/jokowipe-agent /usr/local/bin/jokowipe-agent
RUN chmod +x /usr/local/bin/jokowipe-agent

# Switch to non-root user
USER appuser

# Set entrypoint
ENTRYPOINT ["/usr/local/bin/jokowipe-agent"]

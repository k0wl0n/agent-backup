# Agent Backup

Open-source database backup agent for automated, encrypted backups of PostgreSQL, MySQL, MongoDB, and Redis databases.

## Features

- **Multi-database support**: PostgreSQL, MySQL/MariaDB, MongoDB, Redis
- **Encrypted backups**: AES-256-GCM encryption with platform-managed keys
- **Cloud storage**: S3-compatible storage (AWS S3, Cloudflare R2, MinIO, etc.)
- **Firewall-friendly**: Gateway polling mode for agents behind NAT/firewalls
- **Observability**: OpenTelemetry integration for traces and metrics
- **Resilient**: Circuit breaker pattern, exponential backoff, automatic retries

## Quick Start

### Docker (Recommended)

```bash
docker run -d \
  --name backup-agent \
  -e JOKOWIPE_API_KEY=your-api-key \
  -e JOKOWIPE_SERVER_URL=https://api.jokowipe.id \
  -v /var/backups:/var/lib/jokowipe/backups \
  kowlon/jkwipe-agent:latest
```

### Binary Installation

Download the latest release for your platform:

```bash
# Linux (AMD64)
curl -LO https://github.com/k0wl0n/agent-backup/releases/latest/download/agent-linux-amd64
chmod +x agent-linux-amd64
sudo mv agent-linux-amd64 /usr/local/bin/jokowipe-agent

# macOS (ARM64)
curl -LO https://github.com/k0wl0n/agent-backup/releases/latest/download/agent-darwin-arm64
chmod +x agent-darwin-arm64
sudo mv agent-darwin-arm64 /usr/local/bin/jokowipe-agent

# Windows (AMD64)
# Download agent-windows-amd64.exe from releases page
```

Run the agent:

```bash
jokowipe-agent --api-key YOUR_API_KEY --server https://api.jokowipe.id
```

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `JOKOWIPE_API_KEY` | API key for authentication (required) | - |
| `JOKOWIPE_SERVER_URL` | Control plane URL | `https://api.jokowipe.id` |
| `JOKOWIPE_AGENT_NAME` | Agent name for identification | hostname |
| `JOKOWIPE_AGENT_TYPE` | Agent type: `host`, `headless`, `managed-runner` | `host` |

### Configuration File

Create `agent.yaml`:

```yaml
agent:
  name: "my-backup-agent"
  api_key: "your-api-key-here"

gateway:
  enabled: true  # Enable polling mode (firewall friendly)

storage:
  target_folder: "/var/lib/jokowipe/backups"
  retention_days: 7
  
  # Optional: S3-compatible storage
  s3:
    bucket: "my-backup-bucket"
    region: "us-east-1"
    access_key_id: "..."
    secret_access_key: "..."
    endpoint: "https://s3.amazonaws.com"  # Optional for S3-compatible services

telemetry:
  enabled: false  # Enable OpenTelemetry tracing
  endpoint: "https://otel-collector:4318"
```

Run with config file:

```bash
jokowipe-agent --config agent.yaml
```

## Supported Databases

### PostgreSQL
- PostgreSQL 9.6+
- AWS RDS PostgreSQL
- Supabase
- Neon

### MySQL/MariaDB
- MySQL 5.7+
- MariaDB 10.3+
- AWS RDS MySQL

### MongoDB
- MongoDB 4.0+
- MongoDB Atlas

### Redis
- Redis 5.0+
- AWS ElastiCache

## Architecture

The agent operates in **gateway mode** (polling), making it ideal for environments behind NAT or firewalls:

1. Agent polls the control plane for backup tasks
2. Receives encrypted database credentials
3. Executes backup using native tools (`pg_dump`, `mysqldump`, `mongodump`, `redis-cli`)
4. Encrypts backup with AES-256-GCM
5. Uploads to S3-compatible storage
6. Reports completion status

## Security

- **Encrypted credentials**: Database credentials are encrypted at rest and in transit
- **Platform-managed keys**: Encryption keys are derived server-side, never stored on agent
- **Least privilege**: Agent runs as non-root user in Docker
- **No secrets in config**: All sensitive data provided via environment variables or API

## Development

### Build from Source

```bash
git clone https://github.com/k0wl0n/agent-backup.git
cd agent-backup
go build -o jokowipe-agent ./cmd/agent
```

### Run Tests

```bash
go test -race -count=1 ./...
```

### Local Development

```bash
# Start test databases
docker compose up -d

# Run agent locally
go run ./cmd/agent --config agent.local.yaml
```

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

MIT License - see [LICENSE](LICENSE) for details.

## Support

- **Issues**: [GitHub Issues](https://github.com/k0wl0n/agent-backup/issues)
- **Documentation**: [Wiki](https://github.com/k0wl0n/agent-backup/wiki)
- **Community**: [Discussions](https://github.com/k0wl0n/agent-backup/discussions)

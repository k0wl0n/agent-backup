# Jokowipe Agent

Open-source database backup agent for automated, encrypted backups of PostgreSQL, MySQL, MongoDB, and Redis.

---

## Table of Contents

- [How It Works](#how-it-works)
- [Method 1: Create Agent from the UI (Recommended)](#method-1-create-agent-from-the-ui-recommended)
- [Method 2: Create Agent Manually with config.yaml](#method-2-create-agent-manually-with-configyaml)
- [Method 3: Docker](#method-3-docker)
- [Method 4: Kubernetes (Helm)](#method-4-kubernetes-helm)
- [Configuration Reference](#configuration-reference)
- [Supported Databases](#supported-databases)
- [Development](#development)

---

## How It Works

1. The agent runs on your server and **polls** the Jokowipe control plane for backup tasks (firewall-friendly — no inbound ports needed).
2. When a task arrives, it connects to your database, runs a dump, encrypts it with AES-256-GCM, and uploads it.
3. You monitor, download, and restore backups from the dashboard.

---

## Method 1: Create Agent from the UI (Recommended)

This is the fastest way. The UI generates an API key and installation command for you.

### Step 1 — Get your API key from the dashboard

1. Log in at **https://jokowipe.id**
2. Go to **Agents** in the sidebar
3. Click **Add New Agent**
4. Give the agent a name (e.g. `production-server`)
5. Click **Create** — you'll see a unique API key generated for this agent

> ⚠️ Copy the API key now. It is only shown once.

### Step 2 — Copy the install command

The dashboard shows a ready-to-paste shell command like:

```bash
curl -sL https://api.jokowipe.id/install.sh | sudo bash -s -- \
  --token eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9... \
  --server https://api.jokowipe.id
```

### Step 3 — Run it on your server

SSH into your server and paste the command. The installer will:

- Download the agent binary for your OS/arch
- Create `/etc/jokowipe/agent.yaml` with your token pre-filled
- Register the agent as a **systemd service** (Linux) or **launchd daemon** (macOS)
- Start the agent immediately

### Step 4 — Verify it's running

```bash
# Linux
sudo systemctl status jokowipe-agent

# macOS
sudo launchctl list | grep jokowipe

# Check logs
sudo journalctl -u jokowipe-agent -f      # Linux
tail -f /var/log/jokowipe/agent.log       # macOS
```

The agent will appear as **Online** in the dashboard within 15 seconds.

---

## Method 2: Create Agent Manually with config.yaml

Use this method if you want full control — no installer script, no UI wizard.

### Step 1 — Get an API key

1. Log in at **https://jokowipe.id**
2. Go to **Agents** → **Add New Agent**
3. Enter a name and click **Create**
4. **Copy the API key** (shown once)

### Step 2 — Download the agent binary

```bash
# Linux (AMD64)
curl -LO https://github.com/k0wl0n/agent-backup/releases/latest/download/agent-linux-amd64
chmod +x agent-linux-amd64
sudo mv agent-linux-amd64 /usr/local/bin/jokowipe-agent

# Linux (ARM64)
curl -LO https://github.com/k0wl0n/agent-backup/releases/latest/download/agent-linux-arm64
chmod +x agent-linux-arm64
sudo mv agent-linux-arm64 /usr/local/bin/jokowipe-agent

# macOS (Apple Silicon)
curl -LO https://github.com/k0wl0n/agent-backup/releases/latest/download/agent-darwin-arm64
chmod +x agent-darwin-arm64
sudo mv agent-darwin-arm64 /usr/local/bin/jokowipe-agent

# macOS (Intel)
curl -LO https://github.com/k0wl0n/agent-backup/releases/latest/download/agent-darwin-amd64
chmod +x agent-darwin-amd64
sudo mv agent-darwin-amd64 /usr/local/bin/jokowipe-agent
```

### Step 3 — Create agent.yaml

Copy `agent.example.yaml` and fill in your values:

```bash
cp agent.example.yaml agent.yaml
```

Minimal working config:

```yaml
agent:
  name: "my-server"               # Friendly name shown in the dashboard
  api_key: "paste-your-key-here"  # From Step 1
  type: "host"                    # "host" for bare-metal/VM, "managed-runner" for CI

gateway:
  enabled: true  # Poll mode — no inbound firewall rules needed

storage:
  target_folder: "/var/lib/jokowipe/backups"  # Where to store backup files locally
  retention_days: 7                           # Local copies kept for 7 days (0 = forever)
```

Full config with all optional sections:

```yaml
agent:
  name: "my-server"
  api_key: "paste-your-key-here"
  type: "host"
  log_file: "/var/log/jokowipe/agent.log"  # Omit to log to stdout

gateway:
  enabled: true

storage:
  target_folder: "/var/lib/jokowipe/backups"
  retention_days: 7

  # S3-compatible (AWS S3, Cloudflare R2, MinIO, etc.)
  s3:
    bucket: "my-backup-bucket"
    region: "us-east-1"
    access_key_id: "AKIAIOSFODNN7EXAMPLE"
    secret_access_key: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
    endpoint: "https://s3.amazonaws.com"  # Change for R2/MinIO

  # Google Cloud Storage
  # gcs:
  #   bucket: "my-gcs-bucket"
  #   credentials_file: "/path/to/service-account.json"

  # Azure Blob Storage
  # azure:
  #   account_name: "mystorageaccount"
  #   account_key: "base64-encoded-key"
  #   container: "backups"

telemetry:
  enabled: false
  # endpoint: "https://otel-collector:4318"
  # api_key: "your-otel-api-key"
```

> **Note:** Only configure `s3`, `gcs`, or `azure` if you want an additional copy in cloud storage. The platform always keeps its own encrypted copy regardless.

### Step 4 — Run the agent

```bash
# Run directly
jokowipe-agent --config agent.yaml

# Or with environment variable override (no config file needed)
JOKOWIPE_API_KEY=your-key jokowipe-agent --server https://api.jokowipe.id
```

### Step 5 — (Optional) Run as a systemd service

```ini
# /etc/systemd/system/jokowipe-agent.service
[Unit]
Description=Jokowipe Backup Agent
After=network.target

[Service]
ExecStart=/usr/local/bin/jokowipe-agent --config /etc/jokowipe/agent.yaml
Restart=always
RestartSec=10
User=jokowipe

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now jokowipe-agent
sudo journalctl -u jokowipe-agent -f
```

---

## Method 3: Docker

```bash
docker run -d \
  --name jokowipe-agent \
  --restart unless-stopped \
  -e JOKOWIPE_API_KEY=your-api-key-here \
  -e JOKOWIPE_SERVER_URL=https://api.jokowipe.id \
  -v /var/lib/jokowipe/backups:/var/lib/jokowipe/backups \
  kowlon/jkwipe-agent:latest
```

Or with a mounted config file:

```bash
docker run -d \
  --name jokowipe-agent \
  --restart unless-stopped \
  -v $(pwd)/agent.yaml:/app/agent.yaml:ro \
  -v /var/lib/jokowipe/backups:/var/lib/jokowipe/backups \
  kowlon/jkwipe-agent:latest --config /app/agent.yaml
```

---

## Method 4: Kubernetes (Helm)

Install the agent into your own Kubernetes cluster to back up databases running inside or alongside it.

### Install

```bash
helm install jokowipe-agent oci://ghcr.io/k0wl0n/charts/jokowipe-agent \
  --set agent.apiKey=your-api-key-here \
  --create-namespace -n jokowipe
```

### Access databases on the host node

If your database runs on the node itself (not as a K8s service):

```bash
helm install jokowipe-agent oci://ghcr.io/k0wl0n/charts/jokowipe-agent \
  --set agent.apiKey=your-api-key-here \
  --set hostNetwork=true \
  --create-namespace -n jokowipe
```

### Access databases inside the cluster

Use the Kubernetes DNS name as the host in your backup job configuration:

```
mysql.default.svc.cluster.local:3306
postgres.default.svc.cluster.local:5432
```

### Upgrade

The agent logs a `helm upgrade` command in its output whenever the platform notifies it of a new version:

```bash
helm upgrade jokowipe-agent oci://ghcr.io/k0wl0n/charts/jokowipe-agent \
  --reuse-values \
  --set image.tag=vX.Y.Z \
  -n jokowipe
```

### Key values

| Value | Default | Description |
|---|---|---|
| `agent.apiKey` | `""` | **Required.** Your JokoWipe API key |
| `agent.serverUrl` | `https://api.jokowipe.id` | Platform API URL |
| `agent.name` | pod hostname | Agent name shown in dashboard |
| `agent.existingSecret` | `""` | Use a pre-existing K8s Secret for the API key |
| `image.tag` | chart appVersion | Image tag to deploy |
| `hostNetwork` | `false` | Share host network (to reach host databases) |
| `persistence.enabled` | `true` | Persist backup staging dir via PVC |
| `persistence.size` | `1Gi` | PVC size (backups upload immediately, so 1Gi is enough) |

---

## Configuration Reference

All config fields with their defaults:

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `agent.name` | string | hostname | Friendly name shown in the dashboard |
| `agent.api_key` | string | **required** | API key from the dashboard |
| `agent.type` | string | `host` | `host` or `managed-runner` |
| `agent.log_file` | string | stdout | Path to log file (omit for stdout) |
| `gateway.enabled` | bool | `false` | Enable polling mode (recommended) |
| `storage.target_folder` | string | `./backups` | Local directory for backup files |
| `storage.retention_days` | int | `7` | Days to keep local backups (`0` = forever) |
| `storage.s3.bucket` | string | — | S3 bucket name |
| `storage.s3.region` | string | — | AWS region (e.g. `us-east-1`) |
| `storage.s3.access_key_id` | string | — | S3 access key |
| `storage.s3.secret_access_key` | string | — | S3 secret key |
| `storage.s3.endpoint` | string | — | Custom endpoint (for R2, MinIO, etc.) |
| `storage.gcs.bucket` | string | — | GCS bucket name |
| `storage.gcs.credentials_file` | string | — | Path to GCP service account JSON |
| `storage.azure.account_name` | string | — | Azure storage account name |
| `storage.azure.account_key` | string | — | Azure storage account key (base64) |
| `storage.azure.container` | string | — | Azure blob container name |
| `telemetry.enabled` | bool | `false` | Enable OpenTelemetry tracing |
| `telemetry.endpoint` | string | — | OTLP endpoint (required if enabled) |
| `telemetry.api_key` | string | — | API key for secured collectors |

### Environment Variables

These override config file values — useful for Docker and CI:

| Variable | Overrides |
|----------|-----------|
| `JOKOWIPE_API_KEY` | `agent.api_key` |
| `JOKOWIPE_AGENT_NAME` | `agent.name` |
| `JOKOWIPE_AGENT_TYPE` | `agent.type` |
| `JOKOWIPE_SERVER_URL` | `--server` flag |
| `JOKOWIPE_GATEWAY_ENABLED` | `gateway.enabled` |

---

## Supported Databases

| Database | Versions | Tool Used |
|----------|----------|-----------|
| PostgreSQL | 9.6+ (RDS, Supabase, Neon) | `pg_dump` |
| MySQL / MariaDB | MySQL 5.7+, MariaDB 10.3+ | `mysqldump` |
| MongoDB | 4.0+ (Atlas supported) | `mongodump` |
| Redis | 5.0+ (ElastiCache supported) | `redis-cli` |

---

## Development

### Prerequisites

- Go 1.21+
- Docker (for test databases)

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

### Local Development (with hot-reload)

```bash
# Copy and edit the local config
cp agent.example.yaml agent.local.yaml
# Edit agent.local.yaml with your local API key

# Run with air (hot-reload)
air

# Or run directly against a local backend
go run ./cmd/agent --config agent.local.yaml --server http://localhost:8000
```

---

## Security

- **Encrypted credentials**: Database credentials are encrypted in transit and at rest; the agent never stores them.
- **Platform-managed encryption keys**: Derived server-side from `JOKOWIPE_SECRET_KEY` — never stored on the agent host.
- **No secrets in logs**: Connection strings are redacted before logging.
- **Non-root Docker image**: Agent runs as an unprivileged user inside the container.

---

## Contributing

Contributions are welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

MIT — see [LICENSE](LICENSE) for details.

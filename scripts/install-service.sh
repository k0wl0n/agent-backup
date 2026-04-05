#!/usr/bin/env bash
# Jokowipe Agent — systemd service installer
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/k0wl0n/agent-backup/main/scripts/install-service.sh | sudo bash
#   # or with explicit API key:
#   curl -fsSL .../install-service.sh | sudo bash -s -- --api-key jw_agent_xxxx
set -euo pipefail

# ── colours ───────────────────────────────────────────────────────────────────
green() { printf '\033[32m✔\033[0m  %s\n' "$*"; }
info()  { printf '\033[36mℹ\033[0m  %s\n' "$*"; }
warn()  { printf '\033[33m⚠\033[0m  %s\n' "$*"; }
die()   { printf '\033[31m✖\033[0m  %s\n' "$*" >&2; exit 1; }

# ── root check ───────────────────────────────────────────────────────────────
[[ $EUID -eq 0 ]] || die "Run as root: sudo bash install-service.sh"

# ── defaults ─────────────────────────────────────────────────────────────────
API_KEY=""
CONFIG_FILE="${HOME}/.config/jokowipe/agent.yaml"
BINARY=""
SERVER_URL="https://api.jokowipe.id"
SERVICE_NAME="jokowipe-agent"
SERVICE_USER="root"

# ── parse args ────────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --api-key)   API_KEY="$2";     shift 2 ;;
    --config)    CONFIG_FILE="$2"; shift 2 ;;
    --binary)    BINARY="$2";      shift 2 ;;
    --server)    SERVER_URL="$2";  shift 2 ;;
    --user)      SERVICE_USER="$2";shift 2 ;;
    *) die "Unknown argument: $1" ;;
  esac
done

# ── locate binary ─────────────────────────────────────────────────────────────
if [[ -z "$BINARY" ]]; then
  BINARY=$(command -v jokowipe-agent 2>/dev/null || command -v jokowipe 2>/dev/null || true)
  if [[ -z "$BINARY" ]]; then
    # Common install locations
    for p in /usr/local/bin/jokowipe-agent /root/.local/bin/jokowipe-agent /usr/bin/jokowipe-agent; do
      if [[ -x "$p" ]]; then BINARY="$p"; break; fi
    done
  fi
fi
[[ -n "$BINARY" && -x "$BINARY" ]] || die "Cannot find jokowipe-agent binary. Install it first or pass --binary /path/to/binary"
green "Binary: $BINARY"

# ── resolve config file ───────────────────────────────────────────────────────
# If no explicit config path, try cli.yaml to find configured path
CLI_YAML="${HOME}/.config/jokowipe/cli.yaml"
if [[ -f "$CLI_YAML" ]]; then
  cfg_from_cli=$(grep -m1 '^config_file:' "$CLI_YAML" 2>/dev/null | sed 's/^[^:]*: *//' | tr -d '"' || true)
  [[ -n "$cfg_from_cli" ]] && CONFIG_FILE="$cfg_from_cli"
fi

green "Config: $CONFIG_FILE"

# ── read existing api_key from config ─────────────────────────────────────────
if [[ -z "$API_KEY" && -f "$CONFIG_FILE" ]]; then
  # Parse nested yaml: look for api_key under agent: section
  in_agent=false
  while IFS= read -r line; do
    [[ "$line" =~ ^agent: ]] && in_agent=true && continue
    [[ "$in_agent" == true && "$line" =~ ^[a-z] && ! "$line" =~ ^agent: ]] && in_agent=false
    if [[ "$in_agent" == true && "$line" =~ api_key ]]; then
      API_KEY=$(echo "$line" | sed 's/.*api_key[[:space:]]*:[[:space:]]*//' | tr -d '"' | tr -d "'")
      break
    fi
  done < "$CONFIG_FILE"
fi

# ── ask for API key if still missing ─────────────────────────────────────────
if [[ -z "$API_KEY" ]]; then
  warn "API key not found in $CONFIG_FILE"
  echo -n "  Paste your API key (from dashboard → Agents → Rotate Key): "
  read -r API_KEY
  [[ -n "$API_KEY" ]] || die "API key is required"
fi

# ── validate key format ───────────────────────────────────────────────────────
if [[ "$API_KEY" != jw_agent_* ]] && [[ "$API_KEY" != jw_* ]]; then
  warn "Key doesn't look like a Jokowipe key (expected jw_agent_...). Continuing anyway."
fi

# ── write api_key into config if not already there ───────────────────────────
mkdir -p "$(dirname "$CONFIG_FILE")"
if [[ ! -f "$CONFIG_FILE" ]]; then
  cat > "$CONFIG_FILE" <<YAML
agent:
  api_key: "${API_KEY}"
gateway:
  enabled: true
storage:
  target_folder: /var/lib/jokowipe/backups
  retention_days: 7
YAML
  green "Created $CONFIG_FILE"
else
  # Rewrite the file preserving indentation
  # Use python3 to safely update nested yaml without breaking indentation
  python3 - "$CONFIG_FILE" "$API_KEY" <<'PY'
import sys, re

path, key = sys.argv[1], sys.argv[2]
lines = open(path).readlines()
out = []
in_agent = False
replaced = False

for line in lines:
    stripped = line.lstrip()
    indent = len(line) - len(stripped)

    if re.match(r'^agent\s*:', line):
        in_agent = True
        out.append(line)
        continue

    if in_agent:
        # leaving agent block when a non-indented non-empty line appears
        if stripped and indent == 0 and not re.match(r'^#', stripped):
            in_agent = False
        elif re.match(r'api_key\s*:', stripped):
            out.append(' ' * indent + 'api_key: "' + key + '"\n')
            replaced = True
            continue

    out.append(line)

if not replaced:
    # api_key not found — inject it after "agent:" line
    result = []
    for line in out:
        result.append(line)
        if re.match(r'^agent\s*:', line):
            result.append('  api_key: "' + key + '"\n')
    out = result

open(path, 'w').writelines(out)
print("Updated api_key in", path)
PY
  green "Updated api_key in $CONFIG_FILE"
fi

# ── create backup directory ───────────────────────────────────────────────────
mkdir -p /var/lib/jokowipe/backups

# ── write systemd unit ────────────────────────────────────────────────────────
UNIT_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
cat > "$UNIT_FILE" <<UNIT
[Unit]
Description=Jokowipe Backup Agent
Documentation=https://github.com/k0wl0n/agent-backup
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SERVICE_USER}
Environment=JOKOWIPE_API_KEY=${API_KEY}
ExecStart=${BINARY} --config ${CONFIG_FILE} --server ${SERVER_URL}
Restart=on-failure
RestartSec=10s
StandardOutput=journal
StandardError=journal
SyslogIdentifier=jokowipe-agent

# Hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/var/lib/jokowipe /tmp ${HOME}/.config/jokowipe

[Install]
WantedBy=multi-user.target
UNIT

green "Wrote $UNIT_FILE"

# ── reload + enable + start ───────────────────────────────────────────────────
systemctl daemon-reload
systemctl enable --now "$SERVICE_NAME"

sleep 2
if systemctl is-active --quiet "$SERVICE_NAME"; then
  green "Service is running!"
  echo
  echo "  Status:   systemctl status ${SERVICE_NAME}"
  echo "  Logs:     journalctl -u ${SERVICE_NAME} -f"
  echo "  Stop:     systemctl stop ${SERVICE_NAME}"
  echo "  Restart:  systemctl restart ${SERVICE_NAME}"
else
  warn "Service may have failed to start. Check:"
  echo "  journalctl -u ${SERVICE_NAME} -n 50"
  exit 1
fi

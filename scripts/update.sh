#!/usr/bin/env bash
# JokoWipe Agent — One-line updater (local binary only)
# Usage:  curl -fsSL https://raw.githubusercontent.com/k0wl0n/agent-backup/main/scripts/update.sh | bash
#
# Docker:     docker pull kowlon/jkwipe-agent:latest && docker restart jokowipe-agent
# Kubernetes: kubectl set image deployment/jokowipe-agent agent=kowlon/jkwipe-agent:<version>
set -euo pipefail

# ── guard: not for Docker or Kubernetes ──────────────────────────────────────
if [ -f "/.dockerenv" ] || [ -n "${KUBERNETES_SERVICE_HOST:-}" ]; then
  echo "✖  This script is for local binary installs only." >&2
  if [ -f "/.dockerenv" ]; then
    echo "   Docker: docker pull kowlon/jkwipe-agent:latest && docker restart jokowipe-agent" >&2
  else
    echo "   Kubernetes: kubectl set image deployment/jokowipe-agent agent=kowlon/jkwipe-agent:<version>" >&2
  fi
  exit 1
fi

REPO="k0wl0n/agent-backup"
INSTALL_DIR="${JOKOWIPE_BIN_DIR:-/root/.local/bin}"
BINARY_NAME="jokowipe-agent"
CLI_YAML="${HOME}/.config/jokowipe/cli.yaml"

# ── colours ───────────────────────────────────────────────────────────────────
green()  { printf '\033[32m✔\033[0m  %s\n' "$*"; }
info()   { printf '\033[36mℹ\033[0m  %s\n' "$*"; }
die()    { printf '\033[31m✖\033[0m  %s\n' "$*" >&2; exit 1; }

# ── read cli.yaml values (simple key: value grep, no yaml parser needed) ──────
yaml_val() { grep -m1 "^${1}:" "$CLI_YAML" 2>/dev/null | sed 's/^[^:]*: *//' | tr -d '"' || true; }

AGENT_BIN=$(yaml_val agent_bin)
AGENT_CFG=$(yaml_val config_file)
AGENT_LOG=$(yaml_val log_file)
AGENT_SRV=$(yaml_val server_url)
AGENT_PID=$(yaml_val pid_file)

# Fallback defaults if cli.yaml is missing
AGENT_BIN="${AGENT_BIN:-${INSTALL_DIR}/${BINARY_NAME}}"
AGENT_CFG="${AGENT_CFG:-${HOME}/.config/jokowipe/agent.yaml}"
AGENT_LOG="${AGENT_LOG:-${HOME}/jokowipe.log}"
AGENT_SRV="${AGENT_SRV:-https://api.jokowipe.id}"
AGENT_PID="${AGENT_PID:-${HOME}/.config/jokowipe/agent.pid}"
DEST="${INSTALL_DIR}/${BINARY_NAME}"

# ── detect OS / arch ──────────────────────────────────────────────────────────
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) die "Unsupported architecture: $ARCH" ;;
esac
case "$OS" in
  linux|darwin) ;;
  *) die "Unsupported OS: $OS" ;;
esac

ASSET_NAME="agent-${OS}-${ARCH}"

# ── get latest release tag ────────────────────────────────────────────────────
info "Fetching latest release..."
LATEST=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
  | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\(.*\)".*/\1/')
[ -z "$LATEST" ] && die "Could not determine latest release version"

# ── check current version ─────────────────────────────────────────────────────
CURRENT="(not installed)"
if [ -f "$DEST" ]; then
  CURRENT=$("$DEST" --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1 || echo "unknown")
fi

info "Current: ${CURRENT}   →   Latest: ${LATEST}"
if [ "$CURRENT" = "${LATEST#v}" ] || [ "v$CURRENT" = "$LATEST" ]; then
  green "Already up to date (${LATEST}). Nothing to do."
  exit 0
fi

# ── download binary + checksum ────────────────────────────────────────────────
BASE_URL="https://github.com/${REPO}/releases/download/${LATEST}"
TMP_BIN=$(mktemp)
TMP_SHA=$(mktemp)

info "Downloading ${ASSET_NAME} ${LATEST}..."
curl -fsSL --progress-bar "${BASE_URL}/${ASSET_NAME}"        -o "$TMP_BIN"
curl -fsSL              "${BASE_URL}/${ASSET_NAME}.sha256" -o "$TMP_SHA"

# ── verify SHA256 ─────────────────────────────────────────────────────────────
EXPECTED=$(awk '{print $1}' "$TMP_SHA")
if command -v sha256sum &>/dev/null; then
  ACTUAL=$(sha256sum "$TMP_BIN" | awk '{print $1}')
else
  ACTUAL=$(shasum -a 256 "$TMP_BIN" | awk '{print $1}')
fi

if [ "$EXPECTED" != "$ACTUAL" ]; then
  rm -f "$TMP_BIN" "$TMP_SHA"
  die "SHA256 mismatch! Expected: $EXPECTED  Got: $ACTUAL"
fi
green "Checksum verified"

# ── stop running agent via PID file (no jw dependency) ───────────────────────
info "Stopping agent..."
if [ -f "$AGENT_PID" ]; then
  OLD_PID=$(cat "$AGENT_PID" 2>/dev/null || true)
  if [ -n "$OLD_PID" ] && kill -0 "$OLD_PID" 2>/dev/null; then
    kill -TERM "$OLD_PID" 2>/dev/null || true
    # wait up to 8s for clean exit
    for _ in 1 2 3 4 5 6 7 8; do
      kill -0 "$OLD_PID" 2>/dev/null || break
      sleep 1
    done
    kill -0 "$OLD_PID" 2>/dev/null && kill -KILL "$OLD_PID" 2>/dev/null || true
  fi
  rm -f "$AGENT_PID"
fi

# ── install ───────────────────────────────────────────────────────────────────
mkdir -p "$INSTALL_DIR"
chmod 755 "$TMP_BIN"
[ -f "$DEST" ] && cp "$DEST" "${DEST}.backup"
mv "$TMP_BIN" "$DEST"
rm -f "$TMP_SHA"
green "Installed ${LATEST} → ${DEST}"

# ── start agent directly (no jw dependency) ───────────────────────────────────
info "Starting agent..."
nohup "$DEST" \
  --config  "$AGENT_CFG" \
  --server  "$AGENT_SRV" \
  >> "$AGENT_LOG" 2>&1 &
NEW_PID=$!
echo "$NEW_PID" > "$AGENT_PID"
sleep 1
if kill -0 "$NEW_PID" 2>/dev/null; then
  green "Agent started (PID: ${NEW_PID})"
else
  die "Agent failed to start — check ${AGENT_LOG}"
fi

echo
green "Update complete (${LATEST}). Run: jw status  or  tail -f ${AGENT_LOG}"


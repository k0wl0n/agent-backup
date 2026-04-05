#!/usr/bin/env bash
# JokoWipe Agent — One-line updater
# Usage:  curl -fsSL https://raw.githubusercontent.com/k0wl0n/agent-backup/main/scripts/update.sh | bash
set -euo pipefail

# Detach from curl's stdin so background processes don't inherit the pipe
exec </dev/null

REPO="k0wl0n/agent-backup"
INSTALL_DIR="${JOKOWIPE_BIN_DIR:-/root/.local/bin}"
BINARY_NAME="jokowipe-agent"

# ── colours ───────────────────────────────────────────────────────────────────
green()  { printf '\033[32m✔\033[0m  %s\n' "$*"; }
info()   { printf '\033[36mℹ\033[0m  %s\n' "$*"; }
die()    { printf '\033[31m✖\033[0m  %s\n' "$*" >&2; exit 1; }

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
DEST="${INSTALL_DIR}/${BINARY_NAME}"
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

# ── stop running agent ────────────────────────────────────────────────────────
AGENT_STOPPED=false
if command -v jw &>/dev/null; then
  info "Stopping agent..."
  # timeout 15s so a hung agent never blocks the update
  if timeout 15s jw stop 2>/dev/null; then
    AGENT_STOPPED=true
  else
    # jw stop timed out or failed — kill directly via PID file
    PID_FILE="${HOME}/.config/jokowipe/agent.pid"
    if [ -f "$PID_FILE" ]; then
      PID=$(cat "$PID_FILE" 2>/dev/null || true)
      if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
        kill -TERM "$PID" 2>/dev/null || true
        sleep 2
        kill -KILL "$PID" 2>/dev/null || true
      fi
      rm -f "$PID_FILE"
    fi
    AGENT_STOPPED=true
  fi
fi

# ── install ───────────────────────────────────────────────────────────────────
mkdir -p "$INSTALL_DIR"
chmod 755 "$TMP_BIN"

# Keep a rollback copy
[ -f "$DEST" ] && cp "$DEST" "${DEST}.backup"

mv "$TMP_BIN" "$DEST"
rm -f "$TMP_SHA"

green "Installed ${LATEST} → ${DEST}"

# ── restart agent ─────────────────────────────────────────────────────────────
if $AGENT_STOPPED && command -v jw &>/dev/null; then
  info "Restarting agent..."
  jw start </dev/null
  green "Agent restarted"
fi

echo
green "Update complete. Run: jw status"

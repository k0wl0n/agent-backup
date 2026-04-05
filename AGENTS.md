# Agent Development Rules — jokowipe-agent

This file is read by AI coding agents (GitHub Copilot, Claude, Cursor, etc.) working on this repository.
Follow every rule here strictly. Do not deviate without a comment explaining why.

---

## Repository Layout

```
cmd/agent/main.go          # Binary entrypoint — heartbeat loop, task dispatch, update trigger
internal/
  backup/manager.go        # Core backup logic: dump → archive → encrypt → upload
  client/client.go         # Control-plane HTTP client (register, heartbeat, poll, submit)
  config/config.go         # YAML config loader + Validate()
  update/updater.go        # Auto-update: download from GitHub Releases, verify SHA256, restart
  version/version.go       # Version vars set via ldflags at build time
  cli/                     # jw CLI wrapper (start/stop/status/logs as daemon)
.github/workflows/
  build.yml                # CI: auto-bumps patch version, creates tag, builds Docker image
  release.yml              # Triggered by tag: builds multi-arch binaries + .sha256 files
```

---

## Versioning — Never Tag Manually

**Do not run `git tag` by hand.** `build.yml` auto-calculates and creates the patch tag on every push to `main`.

```bash
# Correct workflow — just push
git add ... && git commit -m "feat|fix|security: ..." && git push origin main

# build.yml then:
# 1. Reads latest vX.Y.Z tag
# 2. Bumps PATCH → vX.Y.(Z+1)
# 3. Creates and pushes the tag
# 4. release.yml triggers → builds binaries + .sha256 → GitHub Release
```

If you manually create a tag and it conflicts, delete the remote tag before the workflow creates it:
```bash
git push origin --delete vX.Y.Z
```

---

## Commit Message Format

Use conventional commits. The CI reads these for release notes.

```
feat: short description       # new capability
fix: short description        # bug fix
security: short description   # security patch (gets its own section in release notes)
refactor: short description
docs: short description
```

Always include the Co-authored-by trailer:
```
Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>
```

---

## Task Types — Allowlist is Enforced

The agent only executes known task types. When adding a new task type, you **must** update the allowlist in `ExecuteTask` in `internal/backup/manager.go`:

```go
switch t.Type {
case "test_connection", "backup_database", "your_new_type":
    // allowed
default:
    // rejected — do not remove this default case
```

Never remove the `default` reject case. A compromised backend must not trigger unknown code paths.

---

## Security Rules — Non-Negotiable

### File Permissions
| Resource | Permission | Reason |
|---|---|---|
| `agent.yaml` | `0600` | Contains API key |
| `jokowipe.log` | `0600` | Leaks S3 paths, task IDs, DB metadata |
| `backups/` staging dir | `0700` | Contains plaintext database dumps |
| Temp `.cnf` files (MySQL creds) | `0600` | Set before write, deleted after use |

When creating any file that may contain secrets or backup data, use `0600`/`0700` — never `0644`/`0755`.

### SSRF Guard — Never Skip
All presigned upload URLs **must** pass `client.ValidateUploadURL()` before the agent issues an HTTP PUT. The allowlist lives in `client/client.go`. To add a new cloud storage provider:

```go
var allowedUploadHosts = []string{
    ".r2.cloudflarestorage.com",
    ".amazonaws.com",
    // add new provider here — must be HTTPS-only
}
```

Do not relax the `https` requirement. Do not add wildcard entries.

### TLS Minimum
The control-plane HTTP client enforces `MinVersion: tls.VersionTLS12`. Do not lower this.

### Response Body Cap
All control-plane responses are read through `io.LimitReader(resp.Body, maxResponseBytes)` (4 MB). Do not bypass this cap when parsing server responses.

### Database Credentials in Process Args
MySQL password is written to a temp `.cnf` file (chmod 0600), never passed as a CLI argument. This prevents credential exposure in `ps aux`. Keep this pattern for any new database type that accepts a password.

---

## Adding a New Database Type

1. Add a new `case` in the `switch strings.ToLower(source.Type)` block in `ExecuteBackup`
2. Write credentials to a temp file (0600), not process args — follow the MySQL pattern
3. Use `buildCmdError` to wrap tool errors
4. Add the type string to `effectivePort()` default map if it has a standard port
5. Update `agent.example.yaml` with the new type and connection example

---

## Adding a New Storage Backend (BYOC)

1. Add a new `case` in `uploadWithByocConfig` in `manager.go`
2. Add the hostname suffix to `allowedUploadHosts` in `client.go`
3. Add config struct to `config.go` + wire into `Config.Storage`
4. Add validation in `Config.Validate()`
5. Never log raw credentials — log only the backend name/type

---

## Auto-Update Flow

The update system in `internal/update/updater.go` is how all installed agents get new versions with zero user effort.

**Flow:**
```
backend heartbeat response → { "update_version": "X.Y.Z" }
agent compares with version.Version
if different → wait for in-flight backup (max 30 min)
→ download binary from GitHub Releases
→ verify SHA256 from companion .sha256 file
→ atomic rename: binary.new → binary (rollback on failure)
→ exec new binary with same args → exit old process
```

**Rules:**
- Always produce a `.sha256` file alongside every binary in the release workflow. `downloadAndVerify` will fail without it.
- Binary download URL pattern: `https://github.com/k0wl0n/agent-backup/releases/download/v{version}/agent-{os}-{arch}`
- Binary names **must** match the `output:` field in `release.yml`'s matrix — they are the same strings used in `releaseURLs()`
- Do not change binary naming without updating `releaseURLs()` in `updater.go`

---

## Build & Test

```bash
# Build
go build ./...

# Test (always run before committing)
go test -race -count=1 ./...

# Build binary with version info (mirrors CI)
go build \
  -ldflags="-X github.com/k0wl0n/agent-backup/internal/version.Version=0.1.3" \
  -o jokowipe-agent \
  ./cmd/agent
```

Tests must pass with the race detector (`-race`). Do not disable the race detector to make tests pass.

---

## What NOT to Do

- **Do not log database passwords** — not even partially masked
- **Do not log presigned URLs** — they carry auth tokens; log only byte counts and S3 paths
- **Do not store credentials in `BackupResult`** — it is serialised and sent back to the backend
- **Do not add `os.Exit()` calls outside of `main.go` and `updater.go`** — makes testing impossible
- **Do not use `ioutil` functions** — deprecated; use `os` and `io` packages directly
- **Do not manually tag releases** — the CI does it; manual tags cause push conflicts
- **Do not lower file permission bits** — 0600/0700 is the minimum for anything sensitive

package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// githubReleaseBase is the GitHub releases download URL prefix.
// Binaries are published here by the release.yml workflow on every tag.
const githubReleaseBase = "https://github.com/k0wl0n/agent-backup/releases/download"

type UpdateHandler struct {
	Version       string
	IsDocker      bool
	BinaryPath    string
	BackupCheckFn func() bool // Returns true if no backups are running
}

// HandleUpdate processes an update request from the server.
// It waits for all backups to complete before proceeding.
func (h *UpdateHandler) HandleUpdate(ctx context.Context, targetVersion string) error {
	log.Printf("[Update] Received update command: %s -> %s", h.Version, targetVersion)

	// Skip if already on target version
	if h.Version == targetVersion || "v"+h.Version == targetVersion || h.Version == "v"+targetVersion {
		log.Printf("[Update] Already on version %s, skipping", h.Version)
		return nil
	}

	// Wait for backups to complete
	log.Printf("[Update] Waiting for running backups to complete...")
	timeout := time.After(30 * time.Minute)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("update cancelled: %w", ctx.Err())
		case <-timeout:
			return fmt.Errorf("update timeout: backups still running after 30 minutes")
		case <-ticker.C:
			if h.BackupCheckFn == nil || h.BackupCheckFn() {
				log.Printf("[Update] No backups running, proceeding with update")
				goto UpdateReady
			}
			log.Printf("[Update] Backups still running, waiting...")
		}
	}

UpdateReady:
	if h.IsDocker {
		return h.updateDocker(targetVersion)
	}
	return h.updateBinary(targetVersion)
}

// updateDocker handles update for Docker-based agents
func (h *UpdateHandler) updateDocker(targetVersion string) error {
	log.Printf("[Update] Docker mode: writing update flag and exiting")

	flagFile := "/var/lib/jokowipe/update-requested"
	if err := os.MkdirAll(filepath.Dir(flagFile), 0700); err != nil {
		return fmt.Errorf("create flag dir: %w", err)
	}

	if err := os.WriteFile(flagFile, []byte(targetVersion), 0600); err != nil {
		return fmt.Errorf("write update flag: %w", err)
	}

	log.Printf("[Update] Update flag set to %s, exiting for Docker restart...", targetVersion)
	time.Sleep(1 * time.Second)
	os.Exit(0)
	return nil
}

// updateBinary downloads the new binary from GitHub Releases, verifies its
// SHA256 checksum, atomically replaces the running binary, and restarts.
func (h *UpdateHandler) updateBinary(targetVersion string) error {
	log.Printf("[Update] Binary mode: downloading version %s", targetVersion)

	downloadURL, checksumURL := h.releaseURLs(targetVersion)
	log.Printf("[Update] Downloading from %s", downloadURL)

	// Download and verify in a temp file
	tmpBinary := h.BinaryPath + ".new"
	if err := h.downloadAndVerify(downloadURL, checksumURL, tmpBinary); err != nil {
		os.Remove(tmpBinary)
		return fmt.Errorf("download failed: %w", err)
	}

	if err := os.Chmod(tmpBinary, 0755); err != nil {
		os.Remove(tmpBinary)
		return fmt.Errorf("chmod binary: %w", err)
	}

	// Atomic replace: backup current → move new into place
	backupBinary := h.BinaryPath + ".backup"
	if err := os.Rename(h.BinaryPath, backupBinary); err != nil {
		os.Remove(tmpBinary)
		return fmt.Errorf("backup current binary: %w", err)
	}

	if err := os.Rename(tmpBinary, h.BinaryPath); err != nil {
		os.Rename(backupBinary, h.BinaryPath) // rollback
		return fmt.Errorf("install new binary: %w", err)
	}
	os.Remove(backupBinary)

	log.Printf("[Update] Binary updated to %s, restarting...", targetVersion)

	cmd := exec.Command(h.BinaryPath, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		os.Rename(backupBinary, h.BinaryPath) // rollback
		return fmt.Errorf("restart agent: %w", err)
	}

	log.Printf("[Update] New agent started (PID %d), exiting old process", cmd.Process.Pid)
	time.Sleep(1 * time.Second)
	os.Exit(0)
	return nil
}

// releaseURLs returns the binary and checksum download URLs for a given version tag.
// Tag may be "0.1.2" or "v0.1.2" — we normalise to the "v" prefix form used by GitHub.
func (h *UpdateHandler) releaseURLs(version string) (binaryURL, checksumURL string) {
	tag := version
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}

	osName := runtime.GOOS
	arch := runtime.GOARCH
	if osName == "darwin" {
		osName = "darwin"
	}
	if arch == "amd64" {
		arch = "amd64"
	}

	// e.g. agent-linux-amd64  or  agent-windows-amd64.exe
	binaryName := fmt.Sprintf("agent-%s-%s", osName, arch)
	if osName == "windows" {
		binaryName += ".exe"
	}

	base := fmt.Sprintf("%s/%s", githubReleaseBase, tag)
	binaryURL = fmt.Sprintf("%s/%s", base, binaryName)
	checksumURL = fmt.Sprintf("%s/%s.sha256", base, binaryName)
	return
}

// downloadAndVerify downloads the binary to destPath and confirms its SHA256
// against the companion .sha256 file published alongside each release binary.
func (h *UpdateHandler) downloadAndVerify(binaryURL, checksumURL, destPath string) error {
	client := &http.Client{Timeout: 10 * time.Minute}

	// Fetch expected checksum first
	expectedHash, err := fetchChecksum(client, checksumURL)
	if err != nil {
		return fmt.Errorf("fetch checksum: %w", err)
	}

	// Download binary
	resp, err := client.Get(binaryURL)
	if err != nil {
		return fmt.Errorf("download binary: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download binary: HTTP %s", resp.Status)
	}

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer f.Close()

	h256 := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h256), resp.Body); err != nil {
		return fmt.Errorf("write binary: %w", err)
	}

	actualHash := hex.EncodeToString(h256.Sum(nil))
	if !strings.EqualFold(actualHash, expectedHash) {
		return fmt.Errorf("checksum mismatch: expected %s got %s", expectedHash, actualHash)
	}

	log.Printf("[Update] Checksum verified: %s", actualHash)
	return nil
}

// fetchChecksum downloads the .sha256 file and returns the hex digest string.
func fetchChecksum(client *http.Client, url string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", err
	}
	// File may be "HASH  filename" (sha256sum format) or just "HASH"
	parts := strings.Fields(string(data))
	if len(parts) == 0 {
		return "", fmt.Errorf("empty checksum file")
	}
	return parts[0], nil
}

// ClearUpdateFlag removes the update flag file after successful update
func ClearUpdateFlag() {
	flagFile := "/var/lib/jokowipe/update-requested"
	if _, err := os.Stat(flagFile); err == nil {
		os.Remove(flagFile)
		log.Printf("[Update] Cleared update flag")
	}
}


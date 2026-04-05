package update

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

type UpdateHandler struct {
	Version        string
	IsDocker       bool
	BinaryPath     string
	BackupCheckFn  func() bool // Returns true if no backups are running
}

// HandleUpdate processes an update request from the server
// It waits for all backups to complete before proceeding
func (h *UpdateHandler) HandleUpdate(ctx context.Context, targetVersion string) error {
	log.Printf("[Update] Received update command: %s -> %s", h.Version, targetVersion)

	// Wait for backups to complete
	log.Printf("[Update] Waiting for running backups to complete...")
	timeout := time.After(30 * time.Minute) // Max 30 minutes wait
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
	
	// Write flag file with target version
	flagFile := "/var/lib/jokowipe/update-requested"
	if err := os.MkdirAll(filepath.Dir(flagFile), 0755); err != nil {
		return fmt.Errorf("create flag dir: %w", err)
	}
	
	if err := os.WriteFile(flagFile, []byte(targetVersion), 0644); err != nil {
		return fmt.Errorf("write update flag: %w", err)
	}
	
	log.Printf("[Update] Update flag set to %s, exiting for Docker restart...", targetVersion)
	
	// Give logs time to flush
	time.Sleep(1 * time.Second)
	
	// Exit gracefully - Docker will restart with new image
	os.Exit(0)
	return nil
}

// updateBinary handles update for standalone binary agents
func (h *UpdateHandler) updateBinary(targetVersion string) error {
	log.Printf("[Update] Binary mode: downloading version %s", targetVersion)
	
	// Determine download URL based on OS and architecture
	downloadURL := h.getDownloadURL(targetVersion)
	log.Printf("[Update] Download URL: %s", downloadURL)
	
	// Download new binary to temp location
	tmpBinary := h.BinaryPath + ".new"
	if err := h.downloadBinary(downloadURL, tmpBinary); err != nil {
		return fmt.Errorf("download binary: %w", err)
	}
	
	// Make it executable
	if err := os.Chmod(tmpBinary, 0755); err != nil {
		os.Remove(tmpBinary)
		return fmt.Errorf("chmod binary: %w", err)
	}
	
	// Backup current binary
	backupBinary := h.BinaryPath + ".backup"
	if err := os.Rename(h.BinaryPath, backupBinary); err != nil {
		os.Remove(tmpBinary)
		return fmt.Errorf("backup current binary: %w", err)
	}
	
	// Move new binary into place
	if err := os.Rename(tmpBinary, h.BinaryPath); err != nil {
		// Rollback
		os.Rename(backupBinary, h.BinaryPath)
		os.Remove(tmpBinary)
		return fmt.Errorf("install new binary: %w", err)
	}
	
	log.Printf("[Update] Binary updated successfully, restarting...")
	
	// Restart the agent
	cmd := exec.Command(h.BinaryPath, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	
	if err := cmd.Start(); err != nil {
		// Rollback on failure
		os.Rename(backupBinary, h.BinaryPath)
		return fmt.Errorf("restart agent: %w", err)
	}
	
	// Exit current process
	log.Printf("[Update] New agent started, exiting old process")
	time.Sleep(1 * time.Second)
	os.Exit(0)
	return nil
}

func (h *UpdateHandler) getDownloadURL(version string) string {
	// Construct download URL based on OS and architecture
	osName := runtime.GOOS
	arch := runtime.GOARCH
	
	// Map to common naming conventions
	if osName == "darwin" {
		osName = "macos"
	}
	if arch == "amd64" {
		arch = "x86_64"
	}
	
	// Example: https://releases.jokowipe.id/agent/v1.2.0/jokowipe-agent-linux-x86_64
	return fmt.Sprintf("https://releases.jokowipe.id/agent/%s/jokowipe-agent-%s-%s",
		version, osName, arch)
}

func (h *UpdateHandler) downloadBinary(url, destPath string) error {
	// TODO: Implement actual download with progress tracking
	// For now, return not implemented
	return fmt.Errorf("binary download not yet implemented - use Docker mode")
}

// ClearUpdateFlag removes the update flag file after successful update
func ClearUpdateFlag() {
	flagFile := "/var/lib/jokowipe/update-requested"
	if _, err := os.Stat(flagFile); err == nil {
		os.Remove(flagFile)
		log.Printf("[Update] Cleared update flag")
	}
}

package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/k0wl0n/agent-backup/internal/backup"
	"github.com/k0wl0n/agent-backup/internal/client"
	"github.com/k0wl0n/agent-backup/internal/config"
	"github.com/k0wl0n/agent-backup/internal/telemetry"
	"github.com/k0wl0n/agent-backup/internal/update"
	"github.com/k0wl0n/agent-backup/internal/version"
)

// taskTracker prevents duplicate task execution with expiry
type taskTracker struct {
	mu       sync.Mutex
	attempts map[string]time.Time
	expiry   time.Duration
}

func newTaskTracker(expiry time.Duration) *taskTracker {
	return &taskTracker{
		attempts: make(map[string]time.Time),
		expiry:   expiry,
	}
}

// shouldRun returns true if the task should be executed
func (tt *taskTracker) shouldRun(taskID string) bool {
	tt.mu.Lock()
	defer tt.mu.Unlock()

	// Clean up expired attempts
	now := time.Now()
	for id, attemptTime := range tt.attempts {
		if now.Sub(attemptTime) > tt.expiry {
			delete(tt.attempts, id)
		}
	}

	// Check if task was recently attempted
	if attemptTime, exists := tt.attempts[taskID]; exists {
		if now.Sub(attemptTime) < tt.expiry {
			return false
		}
	}

	// Mark task as attempted
	tt.attempts[taskID] = now
	return true
}

func main() {
	log.Printf("[Agent] Starting Jokowipe Agent %s", version.Version)

	// Parse CLI flags (passed by the jw CLI wrapper)
	configPath := flag.String("config", "agent.yaml", "Path to agent config file")
	apiKeyFlag := flag.String("api-key", "", "API key (overrides config file)")
	serverURL := flag.String("server", "https://api.jokowipe.id", "Backend server URL")
	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("[Agent] Failed to load config: %v", err)
	}

	// CLI flag overrides config file values
	if *apiKeyFlag != "" {
		cfg.Agent.APIKey = *apiKeyFlag
	}

	// Initialize telemetry if configured
	if cfg.Telemetry.Enabled {
		shutdown, err := telemetry.InitTelemetry(context.Background(), "jokowipe-agent", version.Version, cfg.Telemetry.Endpoint, cfg.Telemetry.APIKey)
		if err != nil {
			log.Printf("[Agent] Failed to initialize telemetry: %v", err)
		} else {
			defer shutdown(context.Background())
		}
	}

	// Initialize client using client.New() to properly initialize HTTPClient
	hostname := cfg.Agent.Name
	if hostname == "" {
		if h, err := os.Hostname(); err == nil {
			hostname = h
		}
	}
	apiClient := client.New(*serverURL, cfg.Agent.APIKey, hostname, cfg.Agent.Type)

	// Register agent
	if err := apiClient.Register(); err != nil {
		log.Fatalf("[Agent] Failed to register: %v", err)
	}
	log.Printf("[Agent] Registered with ID: %s", apiClient.AgentID)

	// Initialize backup manager (pass apiClient so ExecuteTask can submit results)
	backupMgr := backup.New(cfg, apiClient)

	// Initialize task tracker with 10-minute expiry
	// Tasks attempted within the last 10 minutes will be skipped
	tracker := newTaskTracker(10 * time.Minute)

	// Check for pending update flag from previous run
	update.ClearUpdateFlag()

	// Initialize update handler
	isDocker := os.Getenv("DOCKER_CONTAINER") != "" || fileExists("/.dockerenv")
	updateHandler := &update.UpdateHandler{
		Version:    version.Version,
		IsDocker:   isDocker,
		BinaryPath: os.Args[0],
		BackupCheckFn: func() bool {
			return !backupMgr.HasRunningBackups()
		},
	}

	// Setup signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start heartbeat loop
	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()

	// Start task polling loop (if in gateway mode)
	var taskTicker *time.Ticker
	if cfg.Gateway.Enabled {
		taskTicker = time.NewTicker(5 * time.Second)
		defer taskTicker.Stop()
	}

	log.Printf("[Agent] Agent started successfully (Docker: %v)", isDocker)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[Agent] Context cancelled, shutting down...")
			return

		case sig := <-sigChan:
			log.Printf("[Agent] Received signal %v, shutting down gracefully...", sig)
			cancel()
			return

		case <-heartbeatTicker.C:
			resp, err := apiClient.SendHeartbeat()
			if err != nil {
				if err == client.ErrRevoked {
					log.Printf("[Agent] API key revoked, shutting down...")
					cancel()
					return
				}
				log.Printf("[Agent] Heartbeat failed: %v", err)
				continue
			}

			// Check for update command
			if resp != nil && resp.UpdateVersion != nil && *resp.UpdateVersion != "" {
				targetVersion := *resp.UpdateVersion
				if targetVersion != version.Version {
					log.Printf("[Agent] Update requested: %s -> %s", version.Version, targetVersion)
					// Trigger update in background
					go func() {
						if err := updateHandler.HandleUpdate(ctx, targetVersion); err != nil {
							log.Printf("[Agent] Update failed: %v", err)
						}
					}()
				}
			}

		case <-taskTicker.C:
			if !cfg.Gateway.Enabled {
				continue
			}

			task, err := apiClient.PollTask()
			if err != nil {
				if err == client.ErrRevoked {
					log.Printf("[Agent] API key revoked, shutting down...")
					cancel()
					return
				}
				log.Printf("[Agent] Task poll failed: %v", err)
				continue
			}

			if task != nil {
				// Check if task should be executed (not recently attempted)
				if !tracker.shouldRun(task.ID) {
					log.Printf("[Agent] Task %s already attempted recently, skipping", task.ID)
					continue
				}

				log.Printf("[Agent] Received task: %s (type: %s)", task.ID, task.Type)
				go backupMgr.ExecuteTask(ctx, task)
			}
		}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

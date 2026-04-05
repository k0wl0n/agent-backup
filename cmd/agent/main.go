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
	defaultCfgPath := func() string {
		home, err := os.UserHomeDir()
		if err != nil {
			return "agent.yaml"
		}
		return home + "/.config/jokowipe/agent.yaml"
	}()
	configPath := flag.String("config", defaultCfgPath, "Path to agent config file")
	apiKeyFlag := flag.String("api-key", "", "API key (overrides config file)")
	serverURL := flag.String("server", "https://api.jokowipe.id", "Backend server URL")
	flag.Parse()

	// Load configuration (will use defaults if file doesn't exist)
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("[Agent] Failed to load config: %v", err)
	}

	// Warn if the config file is readable by group/other — it contains the API key.
	// On Unix this is a security misconfiguration; recommend 0600.
	if info, err := os.Stat(*configPath); err == nil {
		if info.Mode().Perm()&0o077 != 0 {
			log.Printf("[Agent] WARNING: config file %s has permissions %s — should be 0600 (readable only by owner). Your API key may be exposed.", *configPath, info.Mode().Perm())
		}
	}

	// Detect deployment mode: Kubernetes > Docker > local binary
	isKubernetes := os.Getenv("KUBERNETES_SERVICE_HOST") != ""
	isDocker := !isKubernetes && (os.Getenv("DOCKER_CONTAINER") != "" || fileExists("/.dockerenv"))

	// Environment variables always override config file values.
	// Resolution order (lowest → highest priority): config file → env var → CLI flag.
	if envAPIKey := os.Getenv("JOKOWIPE_API_KEY"); envAPIKey != "" {
		cfg.Agent.APIKey = envAPIKey
	}
	if *apiKeyFlag != "" {
		cfg.Agent.APIKey = *apiKeyFlag
	}
	if envAgentName := os.Getenv("JOKOWIPE_AGENT_NAME"); envAgentName != "" {
		cfg.Agent.Name = envAgentName
	}
	if envAgentType := os.Getenv("JOKOWIPE_AGENT_TYPE"); envAgentType != "" {
		cfg.Agent.Type = envAgentType
	}
	if envServerURL := os.Getenv("JOKOWIPE_SERVER_URL"); envServerURL != "" {
		*serverURL = envServerURL
	}

	// Default agent type when not explicitly configured
	if cfg.Agent.Type == "" {
		cfg.Agent.Type = "host"
	}

	// In Docker/K8s, enable gateway by default so the agent polls for tasks.
	// Users can override with JOKOWIPE_GATEWAY_ENABLED=false if needed.
	if isDocker || isKubernetes {
		if os.Getenv("JOKOWIPE_GATEWAY_ENABLED") == "" {
			cfg.Gateway.Enabled = true
		}
		// Use the container's dedicated backup staging directory when no target is set.
		if cfg.Storage.TargetFolder == "" {
			cfg.Storage.TargetFolder = "/var/lib/jokowipe/backups"
		}
	}

	// Validate configuration after all overrides are applied
	if err := cfg.Validate(); err != nil {
		log.Fatalf("[Agent] %v", err)
	}

	// Redirect log output to file if configured (when running as a daemon via the jw CLI
	// this is handled by the CLI wrapper instead; here we handle the direct-run case).
	if cfg.Agent.LogFile != "" {
		// 0600: log file contains S3 paths, task IDs, and DB connection info
		f, err := os.OpenFile(cfg.Agent.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			log.Printf("[Agent] Warning: cannot open log file %s: %v — logging to stdout", cfg.Agent.LogFile, err)
		} else {
			log.SetOutput(f)
			defer f.Close()
		}
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
	apiClient := client.New(*serverURL, cfg.Agent.APIKey, hostname, cfg.Agent.Type, version.Version)

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

	// Check for pending update flag from previous run (Docker restart after update)
	update.ClearUpdateFlag()

	// Determine deploy mode for update handling
	deployMode := update.DeployBinary
	if isKubernetes {
		deployMode = update.DeployKubernetes
	} else if isDocker {
		deployMode = update.DeployDocker
	}

	// Auto-update handler: downloads and installs a new binary when the backend
	// sends update_version in the heartbeat response. Waits for in-flight backups
	// to finish before replacing the binary.
	//   Binary → atomic replace + re-exec
	//   Docker → write flag file + exit (container restart policy handles restart)
	//   Kubernetes → log instructions; kubectl set image handles rollout
	updateHandler := &update.UpdateHandler{
		Version:    version.Version,
		Mode:       deployMode,
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
	// Use a nil channel when gateway is disabled — nil channels block forever in select,
	// so the case never fires without panicking.
	var taskTickerC <-chan time.Time
	if cfg.Gateway.Enabled {
		taskTicker := time.NewTicker(5 * time.Second)
		defer taskTicker.Stop()
		taskTickerC = taskTicker.C
	}

	deployModeStr := map[update.DeployMode]string{
		update.DeployBinary:     "local binary",
		update.DeployDocker:     "docker",
		update.DeployKubernetes: "kubernetes",
	}[deployMode]
	log.Printf("[Agent] Agent started successfully (mode: %s)", deployModeStr)

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

			// Trigger auto-update when the backend signals a new version.
			// Runs in a goroutine so it doesn't block the heartbeat loop.
			if resp != nil && resp.UpdateVersion != nil && *resp.UpdateVersion != "" {
				targetVersion := *resp.UpdateVersion
				if targetVersion != version.Version {
					log.Printf("[Agent] Update available: %s -> %s", version.Version, targetVersion)
					go func() {
						if err := updateHandler.HandleUpdate(ctx, targetVersion); err != nil {
							log.Printf("[Agent] Auto-update failed: %v", err)
						}
					}()
				}
			}

		case <-taskTickerC:
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

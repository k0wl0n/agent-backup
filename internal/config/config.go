package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "true" || v == "1" {
		return true
	}
	if v == "false" || v == "0" {
		return false
	}
	return fallback
}

// S3Config holds Amazon S3 (or S3-compatible) credentials.
type S3Config struct {
	AccessKeyID     string `yaml:"access_key_id" json:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key" json:"secret_access_key"`
	Region          string `yaml:"region" json:"region"`
	Endpoint        string `yaml:"endpoint" json:"endpoint"`
	Bucket          string `yaml:"bucket" json:"bucket"`
}

// GCSConfig holds Google Cloud Storage credentials.
type GCSConfig struct {
	CredentialsFile string `yaml:"credentials_file"`
	Bucket          string `yaml:"bucket"`
}

// AzureConfig holds Azure Blob Storage credentials.
type AzureConfig struct {
	AccountName string `yaml:"account_name"`
	AccountKey  string `yaml:"account_key"` // Base64-encoded storage account key
	Container   string `yaml:"container"`
}

type Config struct {
	Agent struct {
		Name    string `yaml:"name"` // Optional friendly name
		APIKey  string `yaml:"api_key"`
		LogFile string `yaml:"log_file"` // Optional logging to file
		Type    string `yaml:"type"`     // host, managed-runner
	} `yaml:"agent"`

	Temporal struct {
		HostPort  string `yaml:"host_port"`
		Namespace string `yaml:"namespace"`
		TLS       struct {
			Enabled    bool   `yaml:"enabled"`
			CertPath   string `yaml:"cert_path"`
			KeyPath    string `yaml:"key_path"`
			CAPath     string `yaml:"ca_path"`
			ServerName string `yaml:"server_name"` // For SNI
		} `yaml:"tls"`
	} `yaml:"temporal"`

	Storage struct {
		// TargetFolder specifies a local directory to store backups.
		// If empty, backups might only be streamed to remote storage (S3/GCS).
		TargetFolder string `yaml:"target_folder"`

		// RetentionDays specifies how long to keep local backups in TargetFolder.
		// 0 means keep forever (or managed by server).
		RetentionDays int `yaml:"retention_days"`

		S3    S3Config    `yaml:"s3"`
		GCS   GCSConfig   `yaml:"gcs"`
		Azure AzureConfig `yaml:"azure"`
	} `yaml:"storage"`

	Gateway struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"gateway"`

	Telemetry struct {
		Enabled  bool   `yaml:"enabled"`
		Endpoint string `yaml:"endpoint"` // OTLP Endpoint (e.g., localhost:4318)
		APIKey   string `yaml:"api_key"`  // For HyperDX or other secured collector
	} `yaml:"telemetry"`

	Database struct {
		// Default database URL for local backups if not provided by server
		URL string `yaml:"url"`
	} `yaml:"database"`

	// Manual Backup Profiles (Name -> ConnectionString)
	Databases map[string]string `yaml:"databases"`

	Security struct {
		// EncryptionKey is platform-managed — do NOT set this manually.
		// The agent fetches it automatically from the backend on startup.
		// The backend derives it from JOKOWIPE_SECRET_KEY so it can transparently
		// decrypt backup files when a user downloads them from the dashboard.
		EncryptionKey string `yaml:"encryption_key"`
	} `yaml:"security"`
}

// Load reads the configuration from the given file path.
// If the file does not exist, it returns a default configuration.
func Load(path string) (*Config, error) {
	cfg := &Config{}

	// Set defaults
	cfg.Temporal.HostPort = "localhost:7233"
	cfg.Temporal.Namespace = "default"
	cfg.Storage.RetentionDays = 7
	cfg.Gateway.Enabled = envBool("JOKOWIPE_GATEWAY_ENABLED", false)

	// Read file
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// It's okay if config file doesn't exist, use defaults
			return cfg, nil
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Parse YAML
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Resolve absolute path for TargetFolder if set
	if cfg.Storage.TargetFolder != "" {
		absPath, err := filepath.Abs(cfg.Storage.TargetFolder)
		if err == nil {
			cfg.Storage.TargetFolder = absPath
		}
	}

	return cfg, nil
}

// Validate checks that required fields are present and that all configured
// values are coherent. It returns a combined error listing every problem found.
func (c *Config) Validate() error {
	var errs []string

	// API key — required for connecting to the control plane
	if c.Agent.APIKey == "" {
		errs = append(errs, "agent.api_key is required (or set JOKOWIPE_API_KEY env var)")
	}

	// Agent type — must be one of the known values when explicitly set
	if c.Agent.Type != "" && c.Agent.Type != "host" && c.Agent.Type != "managed-runner" && c.Agent.Type != "headless" {
		errs = append(errs, fmt.Sprintf("agent.type %q is invalid; must be \"host\", \"headless\", or \"managed-runner\"", c.Agent.Type))
	}

	// Retention days must be non-negative
	if c.Storage.RetentionDays < 0 {
		errs = append(errs, "storage.retention_days must be 0 (keep forever) or a positive number")
	}

	// S3 — if any S3 field is set, the minimum required fields must all be present
	s3 := c.Storage.S3
	if s3.Bucket != "" || s3.AccessKeyID != "" || s3.SecretAccessKey != "" {
		var s3errs []string
		if s3.Bucket == "" {
			s3errs = append(s3errs, "bucket")
		}
		if s3.AccessKeyID == "" {
			s3errs = append(s3errs, "access_key_id")
		}
		if s3.SecretAccessKey == "" {
			s3errs = append(s3errs, "secret_access_key")
		}
		if s3.Region == "" && s3.Endpoint == "" {
			s3errs = append(s3errs, "region (or endpoint for S3-compatible services)")
		}
		if len(s3errs) > 0 {
			errs = append(errs, fmt.Sprintf("storage.s3 is incomplete — missing: %s", strings.Join(s3errs, ", ")))
		}
	}

	// GCS — if configured, both fields are required
	gcs := c.Storage.GCS
	if gcs.Bucket != "" || gcs.CredentialsFile != "" {
		if gcs.Bucket == "" {
			errs = append(errs, "storage.gcs.bucket is required when gcs is configured")
		}
		if gcs.CredentialsFile == "" {
			errs = append(errs, "storage.gcs.credentials_file is required when gcs is configured")
		} else if _, err := os.Stat(gcs.CredentialsFile); os.IsNotExist(err) {
			errs = append(errs, fmt.Sprintf("storage.gcs.credentials_file %q does not exist", gcs.CredentialsFile))
		}
	}

	// Azure — if any Azure field is set, all three are required
	az := c.Storage.Azure
	if az.AccountName != "" || az.AccountKey != "" || az.Container != "" {
		var azErrs []string
		if az.AccountName == "" {
			azErrs = append(azErrs, "account_name")
		}
		if az.AccountKey == "" {
			azErrs = append(azErrs, "account_key")
		}
		if az.Container == "" {
			azErrs = append(azErrs, "container")
		}
		if len(azErrs) > 0 {
			errs = append(errs, fmt.Sprintf("storage.azure is incomplete — missing: %s", strings.Join(azErrs, ", ")))
		}
	}

	// Telemetry — endpoint is required when telemetry is enabled
	if c.Telemetry.Enabled && c.Telemetry.Endpoint == "" {
		errs = append(errs, "telemetry.endpoint is required when telemetry.enabled is true")
	}

	if len(errs) > 0 {
		return errors.New("config validation failed:\n  - " + strings.Join(errs, "\n  - "))
	}
	return nil
}

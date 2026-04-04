package config

import (
	"fmt"
	"os"
	"path/filepath"

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

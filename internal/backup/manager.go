package backup

import (
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v5"
	agentConfig "github.com/k0wl0n/agent-backup/internal/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// storageHTTPClient is used for all backup upload HTTP calls (S3, GCS, Azure, R2).
// The 10-minute timeout accommodates large multi-GB backup transfers without
// false-positive timeouts, while still preventing indefinite hangs.
var storageHTTPClient = &http.Client{Timeout: 10 * time.Minute}

// Re-define structs to avoid circular dependencies or backend imports
type SourceConfig struct {
	Type     string `json:"type"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Database string `json:"database"`
}

// ByocStorageConfig holds decrypted BYOC storage credentials injected by the backend.
// The backend fetches and decrypts the user's stored StorageConfig and embeds it here
// so the agent can upload directly to the user's own storage bucket.
type ByocStorageConfig struct {
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

type BackupDefinition struct {
	ID              string          `json:"id"`
	AgentID         string          `json:"agent_id"`
	Name            string          `json:"name"`
	Type            string          `json:"type"` // "database", "file"
	SourceConfig    json.RawMessage `json:"source_config"`
	StorageID       string          `json:"storage_id"`
	StoragePath     string          `json:"storage_path"`
	ScheduleType    string          `json:"schedule_type"`
	CronExp         string          `json:"cron_exp"`
	Retention       json.RawMessage `json:"retention"`
	Compression     bool            `json:"compression"`
	Encryption      bool            `json:"encryption"`
	ArchivePassword string          `json:"archive_password,omitempty"`
	Paused          bool            `json:"paused"`
	CreatedAt       time.Time       `json:"created_at"`

	// Platform-managed upload coordinates injected by the backend.
	// The agent uses PlatformUploadURL (a presigned PUT URL) to upload the
	// backup file directly to R2 — no raw R2 credentials are ever sent.
	PlatformUploadURL string `json:"platform_upload_url,omitempty"`
	PlatformS3Path    string `json:"platform_s3_path,omitempty"`

	// ByocStorageConfig holds decrypted BYOC credentials when the backup uses
	// a user-supplied storage backend instead of the platform R2.
	ByocStorageConfig *ByocStorageConfig `json:"byoc_storage_config,omitempty"`
}

type BackupResult struct {
	Status    string
	Size      int64
	Duration  time.Duration
	S3Path    string
	LocalPath string
	ErrorMsg  string
}

type BackupManager struct {
	cfg *agentConfig.Config
}

func New(cfg *agentConfig.Config) *BackupManager {
	return &BackupManager{cfg: cfg}
}

// effectivePort returns the standard port for the database type if port is 0.
func effectivePort(src SourceConfig) int {
	if src.Port > 0 {
		return src.Port
	}
	switch strings.ToLower(src.Type) {
	case "postgres", "postgresql", "aws_rds", "supabase", "neon":
		return 5432
	case "mysql", "mariadb":
		return 3306
	case "mongodb", "mongo":
		return 27017
	case "redis":
		return 6379
	}
	return 0
}

func (bm *BackupManager) ExecuteBackup(ctx context.Context, def BackupDefinition, logFn func(level, component, message string)) (*BackupResult, error) {
	if logFn == nil {
		logFn = func(_, _, _ string) {}
	}
	ctx, span := otel.Tracer("jokowipe-agent").Start(ctx, "ExecuteBackup")
	defer span.End()

	span.SetAttributes(
		attribute.String("backup.id", def.ID),
		attribute.String("backup.name", def.Name),
		attribute.String("backup.type", def.Type),
		attribute.String("agent.id", def.AgentID),
	)

	// Parse Source Config
	var source SourceConfig
	if err := json.Unmarshal(def.SourceConfig, &source); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed to parse source config")
		return nil, fmt.Errorf("failed to parse source config: %w", err)
	}

	logFn("info", "Scheduler", fmt.Sprintf("Performing backup: %s", def.Name))

	span.SetAttributes(
		attribute.String("db.system", source.Type),
		attribute.String("db.name", source.Database),
		attribute.String("db.user", source.User),
		attribute.String("net.peer.name", source.Host),
		attribute.Int("net.peer.port", source.Port),
	)

	// Determine backup directory: use StoragePath if set, otherwise default to /var/lib/jokowipe/backups
	backupDir := def.StoragePath
	if backupDir == "" || backupDir == "/" {
		backupDir = "/var/lib/jokowipe/backups"
	} else if !filepath.IsAbs(backupDir) {
		backupDir = "/" + backupDir
	}

	// Determine Command based on Source Type
	timestamp := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("%s_%s_%s.dump", def.Name, source.Type, timestamp)
	localPath := filepath.Join(backupDir, filename)
	port := effectivePort(source)

	// Ensure backups dir exists
	logFn("info", "Model", fmt.Sprintf("WorkDir: %s", localPath))
	logFn("info", "Model", fmt.Sprintf("Source: %s://%s@%s:%d/%s", source.Type, source.User, source.Host, port, source.Database))
	if def.Compression {
		logFn("info", "Compressor", "Compression enabled")
	}
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create backups dir: %w", err)
	}

	var stderrBuf bytes.Buffer
	startTime := time.Now()

	switch strings.ToLower(source.Type) {

	// -------------------------------------------------------------------------
	// PostgreSQL and managed Postgres services (AWS RDS, Supabase, Neon)
	// -------------------------------------------------------------------------
	case "postgres", "postgresql", "aws_rds", "supabase", "neon":
		if source.Database == "all" || source.Database == "" {
			// Dump all databases using pg_dumpall (plain-SQL format)
			if _, err := exec.LookPath("pg_dumpall"); err != nil {
				return nil, fmt.Errorf("pg_dumpall not found in PATH — install postgresql-client: %w", err)
			}
			localPath = filepath.Join(backupDir, fmt.Sprintf("%s_%s_%s_all.sql", def.Name, source.Type, timestamp))
			filename = filepath.Base(localPath)
			logFn("info", "Database", fmt.Sprintf("pg_dumpall -h %s -p %d -U %s", source.Host, port, source.User))
			cmd := exec.CommandContext(ctx, "pg_dumpall",
				"-h", source.Host,
				"-p", fmt.Sprintf("%d", port),
				"-U", source.User,
				"-f", localPath,
			)
			cmd.Env = append(os.Environ(), "PGPASSWORD="+source.Password)
			cmd.Stderr = &stderrBuf
			logFn("info", "Storage", fmt.Sprintf("→ %s", localPath))
			if err := cmd.Run(); err != nil {
				return nil, buildCmdError("pg_dumpall", err, &stderrBuf, logFn)
			}
		} else {
			// Dump a single named database
			if _, err := exec.LookPath("pg_dump"); err != nil {
				return nil, fmt.Errorf("pg_dump not found in PATH — install postgresql-client (Debian/Ubuntu: apt-get install -y postgresql-client; RHEL/CentOS: yum install -y postgresql): %w", err)
			}
			logFn("info", "Database", fmt.Sprintf("pg_dump -h %s -p %d -U %s -d %s -F c", source.Host, port, source.User, source.Database))
			cmd := exec.CommandContext(ctx, "pg_dump",
				"-h", source.Host,
				"-p", fmt.Sprintf("%d", port),
				"-U", source.User,
				"-d", source.Database,
				"-F", "c",
				"-f", localPath,
			)
			cmd.Env = append(os.Environ(), "PGPASSWORD="+source.Password)
			cmd.Stderr = &stderrBuf
			logFn("info", "Storage", fmt.Sprintf("→ %s", localPath))
			if err := cmd.Run(); err != nil {
				return nil, buildCmdError("pg_dump", err, &stderrBuf, logFn)
			}
		}

	// -------------------------------------------------------------------------
	// MySQL and MariaDB
	// -------------------------------------------------------------------------
	case "mysql", "mariadb":
		if _, err := exec.LookPath("mysqldump"); err != nil {
			return nil, fmt.Errorf("mysqldump not found in PATH: %w", err)
		}
		logFn("info", "Database", fmt.Sprintf("mysqldump -h %s -P %d -u %s %s", source.Host, port, source.User, source.Database))
		outfile, err := os.Create(localPath)
		if err != nil {
			return nil, fmt.Errorf("failed to create output file: %w", err)
		}
		defer outfile.Close()
		// Write password to a temp .cnf file so it never appears in process args
		// (visible via `ps aux`). The file is deleted immediately after the command.
		myCnf, cnfErr := os.CreateTemp("", "jw-mysql-*.cnf")
		if cnfErr != nil {
			return nil, fmt.Errorf("failed to create temp credentials file: %w", cnfErr)
		}
		if err := os.Chmod(myCnf.Name(), 0600); err != nil {
			myCnf.Close()
			os.Remove(myCnf.Name())
			return nil, fmt.Errorf("failed to secure temp credentials file: %w", err)
		}
		_, _ = fmt.Fprintf(myCnf, "[client]\npassword=%s\n", source.Password)
		myCnf.Close()
		defer os.Remove(myCnf.Name())

		var args []string
		if source.Database == "all" || source.Database == "" {
			args = []string{"--defaults-extra-file=" + myCnf.Name(), "-h", source.Host, "-P", fmt.Sprintf("%d", port), "-u", source.User, "--all-databases"}
		} else {
			args = []string{"--defaults-extra-file=" + myCnf.Name(), "-h", source.Host, "-P", fmt.Sprintf("%d", port), "-u", source.User, source.Database}
		}
		cmd := exec.CommandContext(ctx, "mysqldump", args...)
		cmd.Stdout = outfile
		cmd.Stderr = &stderrBuf
		logFn("info", "Storage", fmt.Sprintf("→ %s", localPath))
		if err := cmd.Run(); err != nil {
			return nil, buildCmdError("mysqldump", err, &stderrBuf, logFn)
		}

	// -------------------------------------------------------------------------
	// MongoDB
	// -------------------------------------------------------------------------
	case "mongodb", "mongo":
		if _, err := exec.LookPath("mongodump"); err != nil {
			return nil, fmt.Errorf("mongodump not found in PATH — install mongodb-database-tools: %w", err)
		}
		filename = fmt.Sprintf("%s_%s_%s.archive", def.Name, source.Type, timestamp)
		localPath = filepath.Join(backupDir, filename)
		// Build args — use --uri when credentials are present so that the password
		// is never exposed in process args (visible via `ps aux` to other users).
		var args []string
		if source.User != "" {
			mongoURI := fmt.Sprintf("mongodb://%s:%s@%s:%d/?authSource=admin",
				url.QueryEscape(source.User),
				url.QueryEscape(source.Password),
				source.Host,
				port,
			)
			args = []string{
				"--uri", mongoURI,
				fmt.Sprintf("--archive=%s", localPath),
				"--gzip",
			}
		} else {
			args = []string{
				"--host", source.Host,
				"--port", fmt.Sprintf("%d", port),
				fmt.Sprintf("--archive=%s", localPath),
				"--gzip",
			}
		}
		if source.Database != "" && source.Database != "all" {
			args = append(args, "--db", source.Database)
		}
		logFn("info", "Database", fmt.Sprintf("mongodump --host %s:%d %s", source.Host, port, source.Database))
		cmd := exec.CommandContext(ctx, "mongodump", args...)
		cmd.Stderr = &stderrBuf
		logFn("info", "Storage", fmt.Sprintf("→ %s", localPath))
		if err := cmd.Run(); err != nil {
			return nil, buildCmdError("mongodump", err, &stderrBuf, logFn)
		}

	// -------------------------------------------------------------------------
	// Redis
	// -------------------------------------------------------------------------
	case "redis":
		if _, err := exec.LookPath("redis-cli"); err != nil {
			return nil, fmt.Errorf("redis-cli not found in PATH — install redis-tools: %w", err)
		}
		filename = fmt.Sprintf("%s_%s_%s.rdb", def.Name, source.Type, timestamp)
		localPath = filepath.Join(backupDir, filename)
		args := []string{"-h", source.Host, "-p", fmt.Sprintf("%d", port), "--rdb", localPath}
		if source.Password != "" {
			args = append(args, "-a", source.Password)
		}
		logFn("info", "Database", fmt.Sprintf("redis-cli -h %s -p %d --rdb %s", source.Host, port, localPath))
		cmd := exec.CommandContext(ctx, "redis-cli", args...)
		cmd.Stderr = &stderrBuf
		logFn("info", "Storage", fmt.Sprintf("→ %s", localPath))
		if err := cmd.Run(); err != nil {
			return nil, buildCmdError("redis-cli", err, &stderrBuf, logFn)
		}

	default:
		return nil, fmt.Errorf("unsupported source type: %s (supported: postgres, mysql, mariadb, mongodb, redis, aws_rds, supabase, neon)", source.Type)
	}

	duration := time.Since(startTime)

	fileInfo, err := os.Stat(localPath)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed to stat backup file")
		return nil, fmt.Errorf("failed to stat backup file: %w", err)
	}

	// Password-protect backup file with zip if archive_password is set
	if def.ArchivePassword != "" {
		if _, err := exec.LookPath("zip"); err != nil {
			logFn("warn", "Archive", "zip command not found - skipping password protection")
		} else {
			logFn("info", "Archive", "Creating password-protected zip archive")
			zipPath := localPath + ".zip"

			// Use zip with password: zip -P password -j output.zip input_file
			// -P: password, -j: junk paths (store just filename), -q: quiet
			cmd := exec.CommandContext(ctx, "zip", "-P", def.ArchivePassword, "-j", "-q", zipPath, localPath)
			if err := cmd.Run(); err != nil {
				logFn("error", "Archive", fmt.Sprintf("Password protection failed: %v", err))
				span.RecordError(err)
				span.SetStatus(codes.Error, "zip password protection failed")
				return nil, fmt.Errorf("failed to create password-protected archive: %w", err)
			}

			os.Remove(localPath) // remove unprotected file
			localPath = zipPath
			filename = filepath.Base(zipPath)

			// Refresh file size after zipping
			if fi, err2 := os.Stat(localPath); err2 == nil {
				fileInfo = fi
			}
			logFn("info", "Archive", fmt.Sprintf("Password-protected → %s", filepath.Base(zipPath)))
		}
	}

	// Encrypt backup file with AES-256-GCM if requested
	if def.Encryption {
		if bm.cfg.Security.EncryptionKey == "" {
			logFn("error", "Encryptor", "Encryption enabled but no encryption_key configured in agent config")
			span.RecordError(fmt.Errorf("encryption enabled but no key configured"))
			span.SetStatus(codes.Error, "encryption key missing")
			return nil, fmt.Errorf("encryption enabled but no encryption_key configured in agent config")
		}
		logFn("info", "Encryptor", "Encrypting backup with AES-256-GCM")
		encPath, err := encryptBackupFile(localPath, bm.cfg.Security.EncryptionKey)
		if err != nil {
			logFn("error", "Encryptor", fmt.Sprintf("Encryption failed: %v", err))
			span.RecordError(err)
			span.SetStatus(codes.Error, "encryption failed")
			return nil, fmt.Errorf("AES-256 encryption failed: %w", err)
		}
		os.Remove(localPath) // remove unencrypted file
		localPath = encPath
		filename = filepath.Base(encPath)
		// Refresh file size after encryption
		if fi, err2 := os.Stat(localPath); err2 == nil {
			fileInfo = fi
		}
		logFn("info", "Encryptor", fmt.Sprintf("Encrypted → %s", filepath.Base(encPath)))
	}

	result := &BackupResult{
		Status:    "success",
		Size:      fileInfo.Size(),
		Duration:  duration,
		LocalPath: localPath,
	}

	// Upload to configured cloud storage backends.
	// Prefix the object key with the agent ID so each agent gets its own
	// folder inside the bucket (e.g. "<agentID>/<filename>").
	objectKey := filename
	if def.AgentID != "" {
		objectKey = def.AgentID + "/" + filename
	}

	// When the task payload includes BYOC credentials, use them for upload.
	// This takes priority over the agent's local storage config.
	if def.ByocStorageConfig != nil {
		logFn("info", "Storage", fmt.Sprintf("Uploading to BYOC storage (%s)", def.ByocStorageConfig.Type))
		cloudPath, err := uploadWithByocConfig(ctx, def.ByocStorageConfig, localPath, objectKey)
		if err != nil {
			logFn("warn", "Storage", fmt.Sprintf("BYOC upload failed: %v", err))
			log.Printf("[backup] BYOC upload failed for %s: %v", def.Name, err)
		} else {
			result.S3Path = cloudPath
			logFn("info", "Storage", fmt.Sprintf("Uploaded → %s", cloudPath))
		}
	} else {
		// No BYOC — use the agent's local storage config (if set) and the platform presigned URL.

		if bm.cfg.Storage.S3.Bucket != "" {
			logFn("info", "Storage", fmt.Sprintf("Uploading to S3 bucket: %s", bm.cfg.Storage.S3.Bucket))
			s3Path, err := uploadToS3(ctx, &bm.cfg.Storage.S3, localPath, objectKey)
			if err != nil {
				logFn("warn", "Storage", fmt.Sprintf("S3 upload failed: %v", err))
				log.Printf("[backup] S3 upload failed for %s: %v", def.Name, err)
			} else {
				logFn("info", "Storage", fmt.Sprintf("Uploaded → %s", s3Path))
				result.S3Path = s3Path
			}
		}

		if bm.cfg.Storage.GCS.Bucket != "" && bm.cfg.Storage.GCS.CredentialsFile != "" {
			logFn("info", "Storage", fmt.Sprintf("Uploading to GCS bucket: %s", bm.cfg.Storage.GCS.Bucket))
			gcsPath, err := uploadToGCS(ctx, &bm.cfg.Storage.GCS, localPath, objectKey)
			if err != nil {
				logFn("warn", "Storage", fmt.Sprintf("GCS upload failed: %v", err))
				log.Printf("[backup] GCS upload failed for %s: %v", def.Name, err)
			} else {
				logFn("info", "Storage", fmt.Sprintf("Uploaded → %s", gcsPath))
				if result.S3Path == "" {
					result.S3Path = gcsPath // reuse the field for any cloud path
				}
			}
		}

		if bm.cfg.Storage.Azure.Container != "" && bm.cfg.Storage.Azure.AccountName != "" {
			logFn("info", "Storage", fmt.Sprintf("Uploading to Azure container: %s", bm.cfg.Storage.Azure.Container))
			azPath, err := uploadToAzure(ctx, &bm.cfg.Storage.Azure, localPath, objectKey)
			if err != nil {
				logFn("warn", "Storage", fmt.Sprintf("Azure upload failed: %v", err))
				log.Printf("[backup] Azure upload failed for %s: %v", def.Name, err)
			} else {
				logFn("info", "Storage", fmt.Sprintf("Uploaded → %s", azPath))
				if result.S3Path == "" {
					result.S3Path = azPath
				}
			}
		}

		// Platform managed storage: upload via presigned PUT URL.
		// The backend injects this for all non-BYOC backups so the platform
		// download endpoint always works. No R2 credentials touch the agent.
		// A failed platform upload is a hard error — the backup file would be
		// unrecoverable from the platform side, so we fail the whole backup.
		if def.PlatformUploadURL != "" {
			if err := platformUpload(ctx, def.PlatformUploadURL, localPath, logFn); err != nil {
				logFn("error", "Storage", fmt.Sprintf("Platform upload failed: %v", err))
				log.Printf("[backup] Platform upload failed for %s: %v", def.Name, err)
				return nil, fmt.Errorf("platform R2 upload failed: %w", err)
			}
			result.S3Path = def.PlatformS3Path
			logFn("info", "Storage", fmt.Sprintf("Uploaded to platform → %s", def.PlatformS3Path))
		}
	}

	logFn("info", "Model", fmt.Sprintf("Backup size: %.2f MB", float64(result.Size)/1024/1024))
	logFn("info", "Scheduler", fmt.Sprintf("Completed in %.2fs", result.Duration.Seconds()))

	span.SetAttributes(
		attribute.Int64("backup.size", result.Size),
		attribute.Float64("backup.duration_ms", float64(result.Duration.Milliseconds())),
	)
	span.SetStatus(codes.Ok, "backup successful")

	// -------------------------------------------------------------------------
	// Local file cleanup — always delete after upload
	// -------------------------------------------------------------------------
	// The local file is only a staging copy for the upload. Once it has been
	// sent to cloud storage there is no reason to keep it on the agent's disk.
	// Always remove it regardless of storage backend or directory.
	if err := os.Remove(localPath); err != nil && !os.IsNotExist(err) {
		log.Printf("[backup] warning: could not remove local staging file %s: %v", localPath, err)
	} else {
		logFn("info", "Storage", "Removed local staging file")
	}

	return result, nil
}

// buildCmdError formats a command execution error with stderr output.
func buildCmdError(tool string, err error, stderrBuf *bytes.Buffer, logFn func(level, component, message string)) error {
	stderrStr := strings.TrimSpace(stderrBuf.String())
	if stderrStr != "" {
		logFn("error", "Database", stderrStr)
		log.Printf("[backup] %s stderr: %s", tool, stderrStr)
		return fmt.Errorf("backup command failed: %w: %s", err, stderrStr)
	}
	return fmt.Errorf("backup command failed: %w", err)
}

// =============================================================================
// AES-256-GCM backup file encryption
// =============================================================================

// encryptBackupFile encrypts srcPath with AES-256-GCM and writes the result to
// srcPath + ".enc". The key must be a 64-character lowercase hex string (32 bytes).
// File format: [12-byte random nonce][ciphertext+16-byte GCM auth tag]
func encryptBackupFile(srcPath, keyHex string) (string, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return "", fmt.Errorf("encryption_key must be a 64-character hex string (32 bytes for AES-256); generate with: openssl rand -hex 32")
	}

	plaintext, err := os.ReadFile(srcPath)
	if err != nil {
		return "", fmt.Errorf("read backup file: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	// 96-bit random nonce (NIST recommended for GCM)
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	// Seal returns nonce || ciphertext || 128-bit auth tag
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)

	encPath := srcPath + ".enc"
	if err := os.WriteFile(encPath, ciphertext, 0600); err != nil {
		return "", fmt.Errorf("write encrypted file: %w", err)
	}
	return encPath, nil
}

// =============================================================================
// Platform presigned PUT upload — no credentials, agent just does HTTP PUT
// =============================================================================

// platformUpload uploads the backup file at localPath to the given presigned PUT URL.
// The URL is generated by the backend (SigV4-signed) and scoped to a single object,
// so the agent never needs R2 credentials.
// Retries up to 3 times with exponential backoff for transient 5xx errors.
func platformUpload(ctx context.Context, presignedURL, localPath string, logFn func(level, component, message string)) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}
	fileSize := fi.Size()

	_, retryErr := backoff.Retry(ctx, func() (struct{}, error) {
		// Seek back to the start for each retry attempt
		if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
			return struct{}{}, backoff.Permanent(fmt.Errorf("seek: %w", seekErr))
		}

		req, err := http.NewRequestWithContext(ctx, "PUT", presignedURL, f)
		if err != nil {
			return struct{}{}, backoff.Permanent(fmt.Errorf("build request: %w", err))
		}
		req.ContentLength = fileSize
		// Do NOT set x-amz-content-sha256 here. The presigned URL already encodes
		// "UNSIGNED-PAYLOAD" as the body hash in its canonical request. Sending this
		// header without including it in the presigned SignedHeaders causes R2/S3 to
		// return 403 SignatureDoesNotMatch.

		logFn("info", "Storage", fmt.Sprintf("PUT %d bytes → platform R2", fileSize))
		resp, err := storageHTTPClient.Do(req)
		if err != nil {
			return struct{}{}, fmt.Errorf("http PUT: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			errMsg := fmt.Errorf("platform upload returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			// 4xx errors are permanent — no point retrying
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				return struct{}{}, backoff.Permanent(errMsg)
			}
			return struct{}{}, errMsg
		}
		return struct{}{}, nil
	}, backoff.WithMaxTries(3))
	return retryErr
}

// =============================================================================
// BYOC storage upload — dispatches to the appropriate backend based on type
// =============================================================================

// uploadWithByocConfig uploads the backup file to the BYOC storage specified in
// the task payload. It supports s3, r2 (S3-compatible), azure, and gcs backends.
func uploadWithByocConfig(ctx context.Context, byoc *ByocStorageConfig, localPath, objectKey string) (string, error) {
	switch strings.ToLower(byoc.Type) {
	case "s3", "r2":
		var cfg agentConfig.S3Config
		if err := json.Unmarshal(byoc.Config, &cfg); err != nil {
			return "", fmt.Errorf("parse S3/R2 config: %w", err)
		}
		// R2 always uses the "auto" region regardless of what the user configured.
		if strings.ToLower(byoc.Type) == "r2" {
			cfg.Region = "auto"
		}
		return uploadToS3(ctx, &cfg, localPath, objectKey)
	case "azure":
		var cfg agentConfig.AzureConfig
		if err := json.Unmarshal(byoc.Config, &cfg); err != nil {
			return "", fmt.Errorf("parse Azure config: %w", err)
		}
		return uploadToAzure(ctx, &cfg, localPath, objectKey)
	case "gcs":
		// GCS requires a credentials file path; write inline credentials_json to
		// a temporary file and pass it to uploadToGCS.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(byoc.Config, &raw); err != nil {
			return "", fmt.Errorf("parse GCS config: %w", err)
		}
		var bucket string
		if b, ok := raw["bucket"]; ok {
			_ = json.Unmarshal(b, &bucket)
		}
		if bucket == "" {
			return "", fmt.Errorf("GCS config missing bucket")
		}
		credJSON, ok := raw["credentials_json"]
		if !ok {
			return "", fmt.Errorf("GCS config missing credentials_json")
		}
		// Unescape the JSON string value to get the raw service-account JSON.
		var credStr string
		if err := json.Unmarshal(credJSON, &credStr); err != nil {
			return "", fmt.Errorf("parse GCS credentials_json: %w", err)
		}
		tmpFile, err := os.CreateTemp("", "gcs-creds-*.json")
		if err != nil {
			return "", fmt.Errorf("create temp credentials file: %w", err)
		}
		defer os.Remove(tmpFile.Name())
		if _, err := tmpFile.WriteString(credStr); err != nil {
			tmpFile.Close()
			return "", fmt.Errorf("write credentials file: %w", err)
		}
		tmpFile.Close()
		cfg := &agentConfig.GCSConfig{
			CredentialsFile: tmpFile.Name(),
			Bucket:          bucket,
		}
		return uploadToGCS(ctx, cfg, localPath, objectKey)
	default:
		return "", fmt.Errorf("unsupported BYOC storage type: %s", byoc.Type)
	}
}

func uploadToS3(ctx context.Context, cfg *agentConfig.S3Config, localPath, objectKey string) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	// Compute payload SHA256 by streaming
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash file: %w", err)
	}
	payloadHash := hex.EncodeToString(h.Sum(nil))

	fileInfo, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}
	fileSize := fileInfo.Size()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek: %w", err)
	}

	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}

	now := time.Now().UTC()
	dateStamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	// Determine host and canonical URI
	var uploadURL, host, canonicalURI string
	if cfg.Endpoint != "" {
		// Custom endpoint (MinIO, Cloudflare R2, etc.) — path style
		ep := strings.TrimRight(cfg.Endpoint, "/")
		uploadURL = fmt.Sprintf("%s/%s/%s", ep, cfg.Bucket, objectKey)
		host = strings.TrimPrefix(strings.TrimPrefix(ep, "https://"), "http://")
		canonicalURI = fmt.Sprintf("/%s/%s", cfg.Bucket, objectKey)
	} else {
		// Real AWS — virtual hosted style
		host = fmt.Sprintf("%s.s3.%s.amazonaws.com", cfg.Bucket, region)
		uploadURL = fmt.Sprintf("https://%s/%s", host, objectKey)
		canonicalURI = "/" + objectKey
	}

	canonicalHeaders := "content-type:application/octet-stream\n" +
		"host:" + host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	signedHeaders := "content-type;host;x-amz-content-sha256;x-amz-date"

	canonicalReq := strings.Join([]string{
		"PUT",
		canonicalURI,
		"", // empty query string
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	credScope := dateStamp + "/" + region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + credScope + "\n" + sha256Hex([]byte(canonicalReq))

	signingKey := s3SigningKey(cfg.SecretAccessKey, dateStamp, region)
	sig := hex.EncodeToString(hmacSHA256bytes(signingKey, []byte(stringToSign)))

	auth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s,SignedHeaders=%s,Signature=%s",
		cfg.AccessKeyID, credScope, signedHeaders, sig)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, f)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.ContentLength = fileSize
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("Authorization", auth)

	resp, err := storageHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("S3 returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return fmt.Sprintf("s3://%s/%s", cfg.Bucket, objectKey), nil
}

func s3SigningKey(secretKey, dateStamp, region string) []byte {
	kDate := hmacSHA256bytes([]byte("AWS4"+secretKey), []byte(dateStamp))
	kRegion := hmacSHA256bytes(kDate, []byte(region))
	kService := hmacSHA256bytes(kRegion, []byte("s3"))
	return hmacSHA256bytes(kService, []byte("aws4_request"))
}

// =============================================================================
// Google Cloud Storage upload — Service Account JWT (standard library only)
// =============================================================================

func uploadToGCS(ctx context.Context, cfg *agentConfig.GCSConfig, localPath, objectKey string) (string, error) {
	token, err := gcsAccessToken(cfg.CredentialsFile)
	if err != nil {
		return "", fmt.Errorf("get GCS token: %w", err)
	}

	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	fileInfo, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}

	uploadURL := fmt.Sprintf(
		"https://storage.googleapis.com/upload/storage/v1/b/%s/o?uploadType=media&name=%s",
		url.PathEscape(cfg.Bucket),
		url.QueryEscape(objectKey),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, f)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.ContentLength = fileInfo.Size()
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := storageHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("GCS returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return fmt.Sprintf("gs://%s/%s", cfg.Bucket, objectKey), nil
}

// gcsAccessToken fetches a short-lived OAuth2 access token from a GCP service account key file.
func gcsAccessToken(credFile string) (string, error) {
	data, err := os.ReadFile(credFile)
	if err != nil {
		return "", fmt.Errorf("read credentials file: %w", err)
	}

	var sa struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal(data, &sa); err != nil {
		return "", fmt.Errorf("parse credentials: %w", err)
	}
	if sa.TokenURI == "" {
		sa.TokenURI = "https://oauth2.googleapis.com/token"
	}

	now := time.Now().Unix()
	header := base64URLEncode([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claimsStr := fmt.Sprintf(
		`{"iss":"%s","sub":"%s","scope":"https://www.googleapis.com/auth/devstorage.read_write","aud":"%s","iat":%d,"exp":%d}`,
		sa.ClientEmail, sa.ClientEmail, sa.TokenURI, now, now+3600,
	)
	claims := base64URLEncode([]byte(claimsStr))

	sigInput := header + "." + claims

	// Parse PEM private key
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM block from private key")
	}

	var rsaKey *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		rsaKey, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return "", fmt.Errorf("parse PKCS1 private key: %w", err)
		}
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return "", fmt.Errorf("parse PKCS8 private key: %w", err)
		}
		var ok bool
		rsaKey, ok = key.(*rsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("expected RSA private key, got %T", key)
		}
	default:
		return "", fmt.Errorf("unsupported PEM block type: %s", block.Type)
	}

	// Sign the JWT
	hashed := sha256.Sum256([]byte(sigInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, hashed[:])
	if err != nil {
		return "", fmt.Errorf("sign JWT: %w", err)
	}

	jwtToken := sigInput + "." + base64URLEncode(sig)

	// Exchange JWT for access token
	resp, err := http.PostForm(sa.TokenURI, url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {jwtToken},
	})
	if err != nil {
		return "", fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("GCS token error: %s — %s", tokenResp.Error, tokenResp.ErrorDesc)
	}

	return tokenResp.AccessToken, nil
}

// =============================================================================
// Azure Blob Storage upload — SharedKey authentication (standard library only)
// =============================================================================

func uploadToAzure(ctx context.Context, cfg *agentConfig.AzureConfig, localPath, objectKey string) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	fileInfo, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}
	fileSize := fileInfo.Size()

	now := time.Now().UTC()
	// Azure requires RFC1123 date in the specific format
	xmsDate := now.Format("Mon, 02 Jan 2006 15:04:05 GMT")
	xmsVersion := "2020-12-06"
	contentType := "application/octet-stream"
	blobType := "BlockBlob"

	blobURL := fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s",
		cfg.AccountName, cfg.Container, objectKey)

	// Canonicalized headers (x-ms-* headers, sorted alphabetically, lowercase)
	canonicalizedHeaders := fmt.Sprintf("x-ms-blob-type:%s\nx-ms-date:%s\nx-ms-version:%s",
		blobType, xmsDate, xmsVersion)

	// Canonicalized resource
	canonicalizedResource := fmt.Sprintf("/%s/%s/%s", cfg.AccountName, cfg.Container, objectKey)

	// String to sign for SharedKey
	// Format: VERB\nContent-Encoding\nContent-Language\nContent-Length\nContent-MD5\nContent-Type\nDate\n
	//         If-Modified-Since\nIf-Match\nIf-None-Match\nIf-Unmodified-Since\nRange\n
	//         CanonicalizedHeaders\nCanonicalizedResource
	stringToSign := strings.Join([]string{
		"PUT",                       // VERB
		"",                          // Content-Encoding
		"",                          // Content-Language
		fmt.Sprintf("%d", fileSize), // Content-Length
		"",                          // Content-MD5
		contentType,
		"", // Date (use x-ms-date instead)
		"", // If-Modified-Since
		"", // If-Match
		"", // If-None-Match
		"", // If-Unmodified-Since
		"", // Range
		canonicalizedHeaders,
		canonicalizedResource,
	}, "\n")

	// Decode the base64 account key and sign
	accountKey, err := base64.StdEncoding.DecodeString(cfg.AccountKey)
	if err != nil {
		return "", fmt.Errorf("decode Azure account key: %w", err)
	}
	mac := hmac.New(sha256.New, accountKey)
	mac.Write([]byte(stringToSign))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	auth := fmt.Sprintf("SharedKey %s:%s", cfg.AccountName, sig)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, blobURL, f)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.ContentLength = fileSize
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("x-ms-blob-type", blobType)
	req.Header.Set("x-ms-date", xmsDate)
	req.Header.Set("x-ms-version", xmsVersion)
	req.Header.Set("Authorization", auth)

	resp, err := storageHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("Azure returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s", cfg.AccountName, cfg.Container, objectKey), nil
}

// =============================================================================
// Shared crypto helpers
// =============================================================================

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func hmacSHA256bytes(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func base64URLEncode(data []byte) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString(data), "=")
}

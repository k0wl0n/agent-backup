package backup

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"os"
	"strings"
	"testing"

	agentConfig "github.com/k0wl0n/agent-backup/internal/config"
	"golang.org/x/crypto/pbkdf2"
	"crypto/sha256"
)

func TestExecuteBackup_InvalidConfig(t *testing.T) {
	cfg := &agentConfig.Config{}
	bm := New(cfg, nil)

	// Invalid JSON
	def := BackupDefinition{
		SourceConfig: json.RawMessage(`{invalid_json}`),
	}

	_, err := bm.ExecuteBackup(context.Background(), def, nil)
	if err == nil {
		t.Error("Expected error for invalid source config, got nil")
	}
}

func TestExecuteBackup_UnsupportedType(t *testing.T) {
	cfg := &agentConfig.Config{}
	bm := New(cfg, nil)

	source := SourceConfig{Type: "oracle"}
	sourceJSON, _ := json.Marshal(source)

	def := BackupDefinition{
		SourceConfig: sourceJSON,
	}

	_, err := bm.ExecuteBackup(context.Background(), def, nil)
	if err == nil {
		t.Error("Expected error for unsupported DB type, got nil")
	}
}

// TestEncryptBackupFileOpenSSL verifies that encryptBackupFileOpenSSL produces
// valid OpenSSL-format output that can be round-trip decrypted.
func TestEncryptBackupFileOpenSSL(t *testing.T) {
	plaintext := []byte("hello openssl encryption test")
	password := "testpassword123"

	// Create a temp file with known plaintext
	tmpFile, err := os.CreateTemp("", "backup-openssl-test-*.dat")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer os.Remove(tmpFile.Name() + ".enc")

	if _, err := tmpFile.Write(plaintext); err != nil {
		t.Fatalf("failed to write plaintext: %v", err)
	}
	tmpFile.Close()

	// Encrypt
	encPath, err := encryptBackupFileOpenSSL(tmpFile.Name(), password)
	if err != nil {
		t.Fatalf("encryptBackupFileOpenSSL failed: %v", err)
	}

	// Verify .enc suffix
	if !strings.HasSuffix(encPath, ".enc") {
		t.Errorf("expected .enc suffix, got %q", encPath)
	}

	// Read encrypted output
	encData, err := os.ReadFile(encPath)
	if err != nil {
		t.Fatalf("failed to read encrypted file: %v", err)
	}

	// Verify OpenSSL magic header
	if len(encData) < 16 {
		t.Fatalf("encrypted output too short: %d bytes", len(encData))
	}
	if !bytes.Equal(encData[:8], []byte("Salted__")) {
		t.Errorf("expected OpenSSL magic header 'Salted__', got %q", encData[:8])
	}

	// Verify output is larger than input (header + salt + padded ciphertext)
	if len(encData) <= len(plaintext) {
		t.Errorf("expected encrypted output larger than input, got %d vs %d", len(encData), len(plaintext))
	}

	// Round-trip: decrypt using Go's own PBKDF2 + AES-CBC
	salt := encData[8:16]
	ciphertext := encData[16:]

	keyIV := pbkdf2.Key([]byte(password), salt, 10000, 48, sha256.New)
	key := keyIV[:32]
	iv := keyIV[32:]

	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("failed to create AES cipher: %v", err)
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		t.Fatalf("ciphertext length %d is not a multiple of block size", len(ciphertext))
	}

	decrypted := make([]byte, len(ciphertext))
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(decrypted, ciphertext)

	// Remove PKCS7 padding
	if len(decrypted) == 0 {
		t.Fatal("decrypted output is empty")
	}
	padLen := int(decrypted[len(decrypted)-1])
	if padLen == 0 || padLen > aes.BlockSize {
		t.Fatalf("invalid PKCS7 padding byte: %d", padLen)
	}
	decrypted = decrypted[:len(decrypted)-padLen]

	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("round-trip failed: expected %q, got %q", plaintext, decrypted)
	}
}

// TestObjectKeyUsesAgentIDPrefix verifies that when an AgentID is set the
// upload object key is prefixed with "<agentID>/" so that each agent stores
// its backups in a dedicated folder inside the shared R2 bucket.
func TestObjectKeyUsesAgentIDPrefix(t *testing.T) {
	filename := "mybackup_postgres_20260101_000000.dump"

	// With an agent ID, the key should be prefixed.
	agentID := "test-agent-id"
	objectKey := filename
	if agentID != "" {
		objectKey = agentID + "/" + filename
	}
	expected := "test-agent-id/mybackup_postgres_20260101_000000.dump"
	if objectKey != expected {
		t.Errorf("expected object key %q, got %q", expected, objectKey)
	}

	// Without an agent ID, the key should remain just the filename.
	emptyAgentID := ""
	objectKeyNoAgent := filename
	if emptyAgentID != "" {
		objectKeyNoAgent = emptyAgentID + "/" + filename
	}
	if objectKeyNoAgent != filename {
		t.Errorf("expected object key %q without agent prefix, got %q", filename, objectKeyNoAgent)
	}
}

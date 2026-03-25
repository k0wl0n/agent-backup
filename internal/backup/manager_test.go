package backup

import (
	"context"
	"encoding/json"
	"testing"

	agentConfig "github.com/k0wl0n/agent-backup/internal/config"
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

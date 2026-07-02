//go:build integration

package backup

import (
	"context"
	"encoding/json"
	"testing"

	agentConfig "github.com/k0wl0n/agent-backup/internal/config"
)

func newIntegrationManager() *BackupManager {
	return New(&agentConfig.Config{}, nil)
}

func logFn(t *testing.T) func(level, component, msg string) {
	t.Helper()
	return func(level, component, msg string) {
		t.Logf("[%s][%s] %s", level, component, msg)
	}
}

func TestIntegration_PostgreSQLBackup(t *testing.T) {
	bm := newIntegrationManager()

	src := SourceConfig{
		Type:     "postgres",
		Host:     "localhost",
		Port:     5432,
		User:     "pguser",
		Password: "pgpassword",
		Database: "sampledb",
	}
	srcJSON, _ := json.Marshal(src)

	def := BackupDefinition{
		ID:           "integration-pg",
		Name:         "integration-pg",
		Type:         "database",
		SourceConfig: srcJSON,
		StoragePath:  t.TempDir(),
	}

	result, err := bm.ExecuteBackup(context.Background(), def, logFn(t))
	if err != nil {
		t.Fatalf("ExecuteBackup: %v", err)
	}
	if result.ErrorMsg != "" {
		t.Errorf("backup reported error: %s", result.ErrorMsg)
	}
	if result.Size == 0 {
		t.Error("backup file size is zero; expected non-empty dump")
	}
}

func TestIntegration_MySQLBackup(t *testing.T) {
	bm := newIntegrationManager()

	src := SourceConfig{
		Type:     "mysql",
		Host:     "localhost",
		Port:     3306,
		User:     "mysqluser",
		Password: "mysqlpassword",
		Database: "sampledb",
	}
	srcJSON, _ := json.Marshal(src)

	def := BackupDefinition{
		ID:           "integration-mysql",
		Name:         "integration-mysql",
		Type:         "database",
		SourceConfig: srcJSON,
		StoragePath:  t.TempDir(),
	}

	result, err := bm.ExecuteBackup(context.Background(), def, logFn(t))
	if err != nil {
		t.Fatalf("ExecuteBackup: %v", err)
	}
	if result.ErrorMsg != "" {
		t.Errorf("backup reported error: %s", result.ErrorMsg)
	}
	if result.Size == 0 {
		t.Error("backup file size is zero; expected non-empty dump")
	}
}

func TestIntegration_MongoDBBackup(t *testing.T) {
	bm := newIntegrationManager()

	src := SourceConfig{
		Type:     "mongodb",
		Host:     "localhost",
		Port:     27017,
		User:     "mongouser",
		Password: "mongopassword",
		Database: "admin",
	}
	srcJSON, _ := json.Marshal(src)

	def := BackupDefinition{
		ID:           "integration-mongo",
		Name:         "integration-mongo",
		Type:         "database",
		SourceConfig: srcJSON,
		StoragePath:  t.TempDir(),
	}

	result, err := bm.ExecuteBackup(context.Background(), def, logFn(t))
	if err != nil {
		t.Fatalf("ExecuteBackup: %v", err)
	}
	if result.ErrorMsg != "" {
		t.Errorf("backup reported error: %s", result.ErrorMsg)
	}
	if result.Size == 0 {
		t.Error("backup file size is zero; expected non-empty archive")
	}
}

func TestIntegration_RedisBackup(t *testing.T) {
	bm := newIntegrationManager()

	src := SourceConfig{
		Type:     "redis",
		Host:     "localhost",
		Port:     6379,
		Password: "redispassword",
	}
	srcJSON, _ := json.Marshal(src)

	def := BackupDefinition{
		ID:           "integration-redis",
		Name:         "integration-redis",
		Type:         "database",
		SourceConfig: srcJSON,
		StoragePath:  t.TempDir(),
	}

	result, err := bm.ExecuteBackup(context.Background(), def, logFn(t))
	if err != nil {
		t.Fatalf("ExecuteBackup: %v", err)
	}
	if result.ErrorMsg != "" {
		t.Errorf("backup reported error: %s", result.ErrorMsg)
	}
	if result.Size == 0 {
		t.Error("backup file size is zero; expected non-empty RDB file")
	}
}

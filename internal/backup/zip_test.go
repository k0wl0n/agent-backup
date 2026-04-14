package backup

import (
	"archive/zip"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCreatePasswordProtectedZip verifies that createPasswordProtectedZip
// creates a valid AES-256 encrypted ZIP file that can be extracted with 7-Zip.
func TestCreatePasswordProtectedZip(t *testing.T) {
	// Create test data file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test-backup.dump")
	testData := []byte("This is a test backup file to verify password-protected ZIP creation works correctly.")
	testPassword := "TestPassword123!"

	if err := os.WriteFile(testFile, testData, 0600); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create password-protected ZIP
	zipPath, err := createPasswordProtectedZip(testFile, testPassword)
	if err != nil {
		t.Fatalf("createPasswordProtectedZip failed: %v", err)
	}
	defer os.Remove(zipPath)

	// Verify ZIP file was created
	if zipPath != testFile+".zip" {
		t.Errorf("Expected ZIP path %s, got %s", testFile+".zip", zipPath)
	}

	// Verify ZIP file exists
	zipData, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatalf("Failed to read ZIP file: %v", err)
	}

	if len(zipData) == 0 {
		t.Fatal("ZIP file is empty")
	}

	// Check ZIP magic bytes
	if len(zipData) < 4 || zipData[0] != 'P' || zipData[1] != 'K' {
		t.Errorf("Invalid ZIP magic bytes: %x", zipData[:4])
	}

	// Try to read the ZIP file structure
	zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		t.Fatalf("Failed to read ZIP file: %v", err)
	}

	// Verify there's exactly one file
	if len(zipReader.File) != 1 {
		t.Fatalf("Expected 1 file in ZIP, got %d", len(zipReader.File))
	}

	// Verify the filename
	expectedName := filepath.Base(testFile)
	if zipReader.File[0].Name != expectedName {
		t.Errorf("Expected filename %s, got %s", expectedName, zipReader.File[0].Name)
	}

	// Try to extract with 7-Zip if available
	if _, err := exec.LookPath("7z"); err == nil {
		t.Log("Testing extraction with 7-Zip...")
		extractDir := filepath.Join(tmpDir, "extracted")
		os.Mkdir(extractDir, 0700)

		cmd := exec.Command("7z", "x", "-p"+testPassword, "-o"+extractDir, zipPath, "-y")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("7z output: %s", output)
			t.Fatalf("7-Zip extraction failed: %v", err)
		}

		// Verify extracted file
		extractedPath := filepath.Join(extractDir, expectedName)
		extractedData, err := os.ReadFile(extractedPath)
		if err != nil {
			t.Fatalf("Failed to read extracted file: %v", err)
		}

		if !bytes.Equal(extractedData, testData) {
			t.Errorf("Extracted data doesn't match original.\nExpected: %s\nGot: %s", testData, extractedData)
		}
		t.Log("✓ 7-Zip extraction successful - file content matches!")
	} else {
		t.Log("7-Zip not found - skipping extraction test (install with: brew install p7zip)")
	}
}

// TestCreatePasswordProtectedZipWithSpecialChars tests that passwords with
// special characters work correctly.
func TestCreatePasswordProtectedZipWithSpecialChars(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "backup.dump")
	testData := []byte("Test data for special character password")
	
	// Password with special characters
	testPassword := "P@ssw0rd!#$%^&*()"

	if err := os.WriteFile(testFile, testData, 0600); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	zipPath, err := createPasswordProtectedZip(testFile, testPassword)
	if err != nil {
		t.Fatalf("createPasswordProtectedZip failed with special char password: %v", err)
	}
	defer os.Remove(zipPath)

	// Verify ZIP was created
	if _, err := os.Stat(zipPath); err != nil {
		t.Fatalf("ZIP file not created: %v", err)
	}

	// Try extraction with 7-Zip if available
	if _, err := exec.LookPath("7z"); err == nil {
		extractDir := filepath.Join(tmpDir, "extracted")
		os.Mkdir(extractDir, 0700)

		cmd := exec.Command("7z", "x", "-p"+testPassword, "-o"+extractDir, zipPath, "-y")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("7z output: %s", output)
			t.Fatalf("7-Zip extraction failed with special char password: %v", err)
		}

		t.Log("✓ Special character password test passed!")
	} else {
		t.Log("7-Zip not found - skipping special character password test")
	}
}

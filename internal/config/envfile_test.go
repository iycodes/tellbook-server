package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEnvFileRejectsDuplicateKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("DUPLICATE_TEST_KEY=first\nDUPLICATE_TEST_KEY=second\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := loadEnvFile(path)
	if err == nil || !strings.Contains(err.Error(), `duplicate environment key "DUPLICATE_TEST_KEY"`) {
		t.Fatalf("loadEnvFile() error = %v", err)
	}
}

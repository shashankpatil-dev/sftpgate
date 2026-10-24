package storage_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/storage"
)

// A file:// storage on a fresh volume must work: the folder doesn't exist
// yet, and temp files must not be written to another disk.
func TestFileStorageOnFreshFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created-yet")
	cfg := &config.Config{Storages: map[string]string{"local": "file://" + dir}}

	reg := storage.NewRegistry()
	if err := reg.Prepare(context.Background(), cfg); err != nil {
		t.Fatalf("a missing folder must be created: %v", err)
	}
	b := reg.Bucket(cfg, "local")
	if err := b.WriteAll(context.Background(), "a/b.txt", []byte("x"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a", "b.txt")); err != nil {
		t.Fatalf("file not written where expected: %v", err)
	}
}

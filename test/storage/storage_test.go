package storage_test

import (
	"bytes"
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

// A normal save is read back and confirmed; the checksum comes back as sent.
func TestPutFileConfirmsTheSave(t *testing.T) {
	cfg := &config.Config{Storages: map[string]string{"m": "mem://"}}
	reg := storage.NewRegistry()
	if err := reg.Prepare(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	b := reg.Bucket(cfg, "m")
	data := []byte("settlement file")

	sha, err := storage.PutFile(context.Background(), b, "a.csv", bytes.NewReader(data), int64(len(data)), storage.PutOptions{})
	if err != nil || len(sha) != 64 {
		t.Fatalf("PutFile = %q, %v", sha, err)
	}
	attrs, _ := b.Attributes(context.Background(), "a.csv")
	if attrs.Metadata["sftpgate-sha256"] != sha {
		t.Fatal("the checksum must be stored with the file")
	}
}

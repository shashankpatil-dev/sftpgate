// Package storage keeps the buckets SFTPgate writes to and reads from open.
package storage

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"gocloud.dev/blob"

	// Bucket drivers, picked by the URL scheme: gs://, s3://, azblob://, file://, mem://
	_ "gocloud.dev/blob/azureblob"
	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/memblob"
	_ "gocloud.dev/blob/s3blob"

	"github.com/shashankpatil-dev/sftpgate/internal/config"
)

// Registry holds one open bucket per storage URL.
//
// Buckets are keyed by URL, not by name. A session that started before a
// reload keeps using the URL from its own config, so it never suddenly
// writes to a different bucket.
//
// shortcut: buckets are never closed until the process exits. They are only
// HTTP clients, so this is cheap; revisit if storage URLs change very often.
type Registry struct {
	mu   sync.Mutex
	open map[string]*blob.Bucket
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{open: map[string]*blob.Bucket{}}
}

// Prepare opens every storage in cfg that isn't open yet.
// A reload is rejected if any of them fails to open.
func (r *Registry) Prepare(ctx context.Context, cfg *config.Config) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for name, url := range cfg.Storages {
		if r.open[url] != nil {
			continue
		}
		b, err := blob.OpenBucket(ctx, withFileDefaults(url))
		if err != nil {
			return fmt.Errorf("storage %s: %w", name, err)
		}
		r.open[url] = b
	}
	return nil
}

// withFileDefaults adds two settings to file:// URLs that are needed when
// the folder is a mounted volume, as in Kubernetes:
//   - create_dir: create the folder if it doesn't exist yet (a new volume is empty)
//   - no_tmp_dir: write temp files next to the target; renaming one from /tmp
//     onto another volume fails with "invalid cross-device link"
func withFileDefaults(url string) string {
	if !strings.HasPrefix(url, "file://") {
		return url
	}
	for _, opt := range []string{"create_dir", "no_tmp_dir"} {
		if strings.Contains(url, opt+"=") {
			continue
		}
		sep := "?"
		if strings.Contains(url, "?") {
			sep = "&"
		}
		url += sep + opt + "=true"
	}
	return url
}

// Bucket returns the open bucket for a storage name in cfg.
// It returns nil if Prepare was not called for that config.
func (r *Registry) Bucket(cfg *config.Config, name string) *blob.Bucket {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.open[cfg.Storages[name]]
}

// Ping checks that every storage in cfg can be reached, for /readyz.
func (r *Registry) Ping(ctx context.Context, cfg *config.Config) error {
	for name := range cfg.Storages {
		b := r.Bucket(cfg, name)
		if b == nil {
			return fmt.Errorf("storage %s: not open", name)
		}
		if _, _, err := b.ListPage(ctx, blob.FirstPageToken, 1, nil); err != nil {
			return fmt.Errorf("storage %s: %w", name, err)
		}
	}
	return nil
}

package storage

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"

	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"
)

// PutOptions change how PutFile writes.
type PutOptions struct {
	// Metadata is stored with the object.
	Metadata map[string]string

	// IfNotExist makes the write fail with ErrExists when the key already
	// exists. The bucket checks this when saving, so it holds even when two
	// uploads, or two SFTPgate pods, race for the same name.
	IfNotExist bool
}

// ErrExists is returned when PutOptions.IfNotExist is set and the key exists.
var ErrExists = errors.New("file already exists")

// maxParallelSends is how many files one SFTPgate sends to storage at the
// same time. Storage libraries copy each file into memory buffers while
// sending it (the S3 SDK keeps up to about 25 MiB per file), so without a
// cap, memory would grow with every partner uploading at once. Partners
// still upload in parallel into the spool; only this last step waits.
//
// shortcut: a fixed number; make it a setting if bigger pods need more.
const maxParallelSends = 4

var sending = make(chan struct{}, maxParallelSends)

// ErrNotKept is returned when the storage said the file was saved, but
// reading it back shows it wasn't (or isn't complete). Some S3-compatible
// stores answer "200 OK" even when they keep nothing, for example when the
// bucket is missing.
var ErrNotKept = errors.New("storage did not keep the file")

// PutFile uploads the first size bytes of r to key and returns their SHA-256.
//
// The data is read twice: once to work out the checksums, once to send it.
// The bucket checks the MD5, so a transfer that got corrupted on the way is
// never saved. The object only appears in the bucket if everything worked.
// The SHA-256 is stored in the object's metadata as "sftpgate-sha256".
//
// After saving, the object is read back (its size and checksum, not its
// data) to make sure the storage really kept it. Only then does PutFile
// report success.
func PutFile(ctx context.Context, b *blob.Bucket, key string, r io.ReaderAt, size int64, opts PutOptions) (string, error) {
	sha, md5sum, err := checksums(io.NewSectionReader(r, 0, size))
	if err != nil {
		return "", err
	}

	metadata := maps.Clone(opts.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["sftpgate-sha256"] = sha

	select {
	case sending <- struct{}{}:
		defer func() { <-sending }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	// Cancelling the context before Close tells the bucket to throw the
	// write away instead of saving it.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	w, err := b.NewWriter(ctx, key, &blob.WriterOptions{
		ContentMD5: md5sum,
		Metadata:   metadata,
		IfNotExist: opts.IfNotExist,
		// Small buffers, one part at a time: memory stays a few MiB per file.
		// 5 MiB is the smallest part size S3 allows.
		BufferSize:     5 << 20,
		MaxConcurrency: 1,
	})
	if err != nil {
		return "", existsOr(err)
	}
	if _, err := io.Copy(w, io.NewSectionReader(r, 0, size)); err != nil {
		cancel()
		_ = w.Close()
		return "", existsOr(err)
	}
	if err := w.Close(); err != nil {
		return "", existsOr(err)
	}

	attrs, err := b.Attributes(ctx, key)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrNotKept, err)
	}
	if attrs.Size != size || attrs.Metadata["sftpgate-sha256"] != sha {
		return "", fmt.Errorf("%w: stored %d bytes, sent %d", ErrNotKept, attrs.Size, size)
	}
	return sha, nil
}

// existsOr turns the bucket's "precondition failed" into ErrExists.
func existsOr(err error) error {
	if gcerrors.Code(err) == gcerrors.FailedPrecondition {
		return ErrExists
	}
	return err
}

func checksums(r io.Reader) (sha string, md5sum []byte, err error) {
	s, m := sha256.New(), md5.New()
	if _, err := io.Copy(io.MultiWriter(s, m), r); err != nil {
		return "", nil, err
	}
	return hex.EncodeToString(s.Sum(nil)), m.Sum(nil), nil
}

package storage

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"maps"

	"gocloud.dev/blob"
)

// PutFile uploads the first size bytes of r to key and returns their SHA-256.
//
// The data is read twice: once to work out the checksums, once to send it.
// The bucket checks the MD5, so a transfer that got corrupted on the way is
// never saved. The object only appears in the bucket if everything worked.
// The SHA-256 is stored in the object's metadata as "sftpgate-sha256".
func PutFile(ctx context.Context, b *blob.Bucket, key string, r io.ReaderAt, size int64, meta map[string]string) (string, error) {
	sha, md5sum, err := checksums(io.NewSectionReader(r, 0, size))
	if err != nil {
		return "", err
	}

	metadata := maps.Clone(meta)
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["sftpgate-sha256"] = sha

	// Cancelling the context before Close tells the bucket to throw the
	// write away instead of saving it.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	w, err := b.NewWriter(ctx, key, &blob.WriterOptions{ContentMD5: md5sum, Metadata: metadata})
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(w, io.NewSectionReader(r, 0, size)); err != nil {
		cancel()
		_ = w.Close()
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return sha, nil
}

func checksums(r io.Reader) (sha string, md5sum []byte, err error) {
	s, m := sha256.New(), md5.New()
	if _, err := io.Copy(io.MultiWriter(s, m), r); err != nil {
		return "", nil, err
	}
	return hex.EncodeToString(s.Sum(nil)), m.Sum(nil), nil
}

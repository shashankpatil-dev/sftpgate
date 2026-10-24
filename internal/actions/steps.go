package actions

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/metrics"
	"github.com/shashankpatil-dev/sftpgate/internal/storage"
	"github.com/shashankpatil-dev/sftpgate/internal/tracing"
)

// current is the file as it moves through the steps. Each step can replace
// it, for example pgp_decrypt swaps the encrypted file for the plain one.
type current struct {
	job      Job
	cfg      *config.Config
	path     string
	filename string
	copies   []Copied
	temps    []string
}

// Copied is a file a copy step saved.
type Copied struct {
	Storage string `json:"storage"`
	Key     string `json:"key"`
}

// runSteps downloads the uploaded file and runs every step of its folder.
// The uploaded file itself is never changed: steps only write new files.
func (r *Runner) runSteps(ctx context.Context, job Job) error {
	cfg := r.Config()
	acc := cfg.Account(job.Account)
	if acc == nil || acc.Folder(job.Folder) == nil {
		return permanent("folder %s of account %s is no longer in the config", job.Folder, job.Account)
	}
	folder := acc.Folder(job.Folder)
	bucket := r.Buckets.Bucket(cfg, job.Storage)
	if bucket == nil {
		return permanent("storage %s is no longer in the config", job.Storage)
	}

	cur := &current{job: job, cfg: cfg, filename: job.Filename}
	defer cur.cleanup()

	if err := r.download(ctx, cur); err != nil {
		return err
	}
	for _, step := range folder.OnUpload {
		stepCtx, span := tracing.Tracer.Start(ctx, "action."+step.Name())
		err := r.runStep(stepCtx, step, cur)
		tracing.End(span, err)
		result := audit.OK
		if err != nil {
			result = audit.Failed
		}
		metrics.Actions.WithLabelValues(step.Name(), result).Inc()
		if err != nil {
			return fmt.Errorf("%s: %w", step.Name(), err)
		}
	}
	return nil
}

func (r *Runner) runStep(ctx context.Context, step *config.Step, cur *current) error {
	switch {
	case step.PGPDecrypt != nil:
		return r.decrypt(cur, step.PGPDecrypt)
	case step.PGPEncrypt != nil:
		return r.encrypt(cur, step.PGPEncrypt)
	case step.Copy != nil:
		return r.copy(ctx, cur, step.Copy)
	case step.Webhook != nil:
		return r.webhook(ctx, cur, step.Webhook)
	}
	return permanent("unknown step")
}

// download fetches the uploaded file into a temp file and checks that it is
// still the exact file that was uploaded.
func (r *Runner) download(ctx context.Context, cur *current) error {
	bucket := r.Buckets.Bucket(cur.cfg, cur.job.Storage)
	rd, err := bucket.NewReader(ctx, cur.job.Key, nil)
	if err != nil {
		return err
	}
	defer closeQuietly(rd)

	f, err := cur.newTemp(r.WorkDir)
	if err != nil {
		return err
	}
	defer closeQuietly(f)

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), rd); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != cur.job.SHA256 {
		return permanent("the file in the bucket changed after it was uploaded (sha256 %s, expected %s)", got, cur.job.SHA256)
	}
	cur.path = f.Name()
	return f.Close()
}

// decrypt replaces the current file with its decrypted contents. Both
// armored (text) and binary PGP files are accepted. A tampered file fails
// its integrity check and is rejected.
//
// shortcut: signatures are not verified; add a signer_keys option if
// partners start signing their files.
func (r *Runner) decrypt(cur *current, d *config.PGPDecrypt) error {
	in, err := os.Open(cur.path)
	if err != nil {
		return err
	}
	defer closeQuietly(in)

	src := io.Reader(bufio.NewReader(in))
	if peek, _ := src.(*bufio.Reader).Peek(30); bytes.HasPrefix(peek, []byte("-----BEGIN PGP MESSAGE")) {
		block, err := armor.Decode(src)
		if err != nil {
			return permanent("bad armored PGP file: %v", err)
		}
		src = block.Body
	}

	md, err := openpgp.ReadMessage(src, d.Keys, nil, nil)
	if err != nil {
		return permanent("cannot decrypt: %v", err)
	}

	out, err := cur.newTemp(r.WorkDir)
	if err != nil {
		return err
	}
	defer closeQuietly(out)
	if _, err := io.Copy(out, md.UnverifiedBody); err != nil {
		return permanent("decrypted data failed its integrity check: %v", err)
	}

	cur.path = out.Name()
	cur.filename = trimPGPExt(cur.filename)
	return out.Close()
}

// encrypt replaces the current file with a copy encrypted for the
// recipients, and adds ".pgp" to its name.
func (r *Runner) encrypt(cur *current, e *config.PGPEncrypt) error {
	in, err := os.Open(cur.path)
	if err != nil {
		return err
	}
	defer closeQuietly(in)

	out, err := cur.newTemp(r.WorkDir)
	if err != nil {
		return err
	}
	defer closeQuietly(out)

	w, err := openpgp.Encrypt(out, e.Keys, nil, &openpgp.FileHints{FileName: cur.filename, IsBinary: true}, nil)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, in); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	cur.path = out.Name()
	cur.filename += ".pgp"
	return out.Close()
}

// copy saves the current file to another storage under a templated key.
// Running it twice writes the same key again, so retries are safe.
func (r *Runner) copy(ctx context.Context, cur *current, c *config.Copy) error {
	bucket := r.Buckets.Bucket(cur.cfg, c.Storage)
	if bucket == nil {
		return permanent("storage %s is no longer in the config", c.Storage)
	}
	key := config.RenderKey(c.Key, cur.vars())

	f, err := os.Open(cur.path)
	if err != nil {
		return err
	}
	defer closeQuietly(f)
	info, err := f.Stat()
	if err != nil {
		return err
	}

	_, err = storage.PutFile(ctx, bucket, key, f, info.Size(), map[string]string{
		"sftpgate-job":    cur.job.ID,
		"sftpgate-source": cur.job.Storage + "/" + cur.job.Key,
	})
	if err != nil {
		return err
	}
	cur.copies = append(cur.copies, Copied{Storage: c.Storage, Key: key})
	return nil
}

// WebhookPayload is the JSON body a webhook receives.
type WebhookPayload struct {
	Event    string    `json:"event"`
	JobID    string    `json:"job_id"`
	Account  string    `json:"account"`
	Folder   string    `json:"folder"`
	Storage  string    `json:"storage"`
	Key      string    `json:"key"`
	Filename string    `json:"filename"`
	Size     int64     `json:"size"`
	SHA256   string    `json:"sha256"`
	Uploaded time.Time `json:"uploaded"`

	// Copies lists the files made by copy steps that ran before this webhook.
	Copies []Copied `json:"copies"`
}

// webhook posts the upload details as JSON.
//
// With a secret, the body is signed: X-Sftpgate-Signature is
// "sha256=" + hex(HMAC-SHA256(secret, body)). A job can run more than once
// after a failure, so receivers should ignore a job_id they already saw.
func (r *Runner) webhook(ctx context.Context, cur *current, w *config.Webhook) error {
	job := cur.job
	body, err := json.Marshal(WebhookPayload{
		Event:    "upload",
		JobID:    job.ID,
		Account:  job.Account,
		Folder:   job.Folder,
		Storage:  job.Storage,
		Key:      job.Key,
		Filename: job.Filename,
		Size:     job.Size,
		SHA256:   job.SHA256,
		Uploaded: job.Uploaded,
		Copies:   append([]Copied{}, cur.copies...),
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return permanent("bad webhook URL: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SFTPgate")
	req.Header.Set("X-Sftpgate-Job", job.ID)
	if w.Secret != "" {
		req.Header.Set("X-Sftpgate-Signature", "sha256="+Sign(w.Secret, body))
	}
	// Lets the receiver continue this trace, when tracing is on.
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := r.Client.Do(req)
	if err != nil {
		return err
	}
	defer closeQuietly(resp.Body)
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook answered %s", resp.Status)
	}
	return nil
}

// Sign returns the hex HMAC-SHA256 of body. Webhook receivers can use the
// same calculation to check that a call really came from SFTPgate.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// vars returns the values for {placeholders} in copy keys.
func (cur *current) vars() map[string]string {
	t := cur.job.Uploaded.In(cur.cfg.Server.Location)
	return map[string]string{
		"account":  cur.job.Account,
		"folder":   strings.TrimPrefix(cur.job.Folder, "/"),
		"date":     t.Format("2006-01-02"),
		"time":     t.Format("150405"),
		"filename": cur.filename,
		"job_id":   cur.job.ID,
	}
}

// newTemp creates a temp file that is deleted when the job ends.
func (cur *current) newTemp(dir string) (*os.File, error) {
	f, err := os.CreateTemp(dir, "work-*")
	if err != nil {
		return nil, err
	}
	cur.temps = append(cur.temps, f.Name())
	return f, nil
}

func (cur *current) cleanup() {
	for _, p := range cur.temps {
		_ = os.Remove(p)
	}
}

// closeQuietly closes c and ignores the error. It is only used for files
// that were just read, or as cleanup for files whose real Close was already
// checked, so the error can't mean lost data.
func closeQuietly(c io.Closer) {
	_ = c.Close()
}

// trimPGPExt drops a .pgp, .gpg or .asc ending from a file name.
func trimPGPExt(name string) string {
	for _, ext := range []string{".pgp", ".gpg", ".asc"} {
		if strings.HasSuffix(strings.ToLower(name), ext) && len(name) > len(ext) {
			return name[:len(name)-len(ext)]
		}
	}
	return name
}

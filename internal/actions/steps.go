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
	"slices"
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

// runSteps runs the actions of the job's hook on a copy of the file. The file
// itself is never changed: actions only write new files.
func (r *Runner) runSteps(ctx context.Context, job Job) error {
	cfg := r.Config()
	steps, err := stepsFor(cfg, job)
	if err != nil {
		return err
	}
	if r.Buckets.Bucket(cfg, job.Storage) == nil {
		return permanent("storage %s is no longer in the config", job.Storage)
	}

	cur := &current{job: job, cfg: cfg, filename: job.Filename}
	defer cur.cleanup()

	// A webhook only sends the file's details, so a hook with only webhooks
	// checks the stored SHA-256 instead of downloading the file.
	switch {
	case slices.ContainsFunc(steps, func(s *config.Step) bool { return s.Webhook == nil }):
		err = r.download(ctx, cur)
	case job.SHA256 != "":
		err = r.unchanged(ctx, cur)
	}
	if err != nil {
		return err
	}

	for _, step := range steps {
		if err := r.traced(ctx, step, cur); err != nil {
			return fmt.Errorf("%s: %w", step.Name(), err)
		}
	}
	return nil
}

// stepsFor finds the actions of the job's hook in the current config, so a
// changed action applies to jobs already in the queue.
func stepsFor(cfg *config.Config, job Job) ([]*config.Step, error) {
	if job.Transfer != "" {
		t := cfg.Transfer(job.Transfer)
		if t == nil {
			return nil, permanent("transfer %s is no longer in the config", job.Transfer)
		}
		return t.Hooks.Steps(job.hook()), nil
	}
	acc := cfg.Account(job.Account)
	if acc == nil || acc.Folder(job.Folder) == nil {
		return nil, permanent("folder %s of account %s is no longer in the config", job.Folder, job.Account)
	}
	return acc.Folder(job.Folder).Hooks.Steps(job.hook()), nil
}

// Before runs the actions of a "before" hook on a local file right away,
// while the upload or transfer waits. It returns the file to use from then
// on (pgp_encrypt makes a new one), its name, and a function that removes
// the temporary files. A failed check returns a *Rejected error.
func (r *Runner) Before(ctx context.Context, steps []*config.Step, path, filename string) (string, string, func(), error) {
	cur := &current{path: path, filename: filename}
	for _, step := range steps {
		if err := r.traced(ctx, step, cur); err != nil {
			return "", "", cur.cleanup, err
		}
	}
	return cur.path, cur.filename, cur.cleanup, nil
}

// traced runs one action in its own span and counts the result.
func (r *Runner) traced(ctx context.Context, step *config.Step, cur *current) error {
	ctx, span := tracing.Tracer.Start(ctx, "action."+step.Name())
	err := r.runStep(ctx, step, cur)
	tracing.End(span, err)
	result := audit.OK
	if err != nil {
		result = audit.Failed
	}
	metrics.Actions.WithLabelValues(step.Name(), result).Inc()
	return err
}

func (r *Runner) runStep(ctx context.Context, step *config.Step, cur *current) error {
	switch {
	case step.Check != nil:
		return checkFile(cur.path, step.Check)
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

// download fetches the file into a temp file and checks that it is still
// the exact file the job was made for. When that isn't known (a file pushed
// to a partner, or one SFTPgate didn't store), its SHA-256 is worked out.
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
	got := hex.EncodeToString(h.Sum(nil))
	switch cur.job.SHA256 {
	case "":
		cur.job.SHA256 = got
	case got:
	default:
		return permanent("the file in the bucket changed after it was uploaded (sha256 %s, expected %s)", got, cur.job.SHA256)
	}
	cur.path = f.Name()
	return f.Close()
}

// unchanged makes sure the file is still the one the job was made for, from
// the SHA-256 that SFTPgate stores with every file it saves.
func (r *Runner) unchanged(ctx context.Context, cur *current) error {
	attrs, err := r.Buckets.Bucket(cur.cfg, cur.job.Storage).Attributes(ctx, cur.job.Key)
	if err != nil {
		return err
	}
	if got := attrs.Metadata["sftpgate-sha256"]; got != cur.job.SHA256 {
		return permanent("the file in the bucket changed after it was uploaded (sha256 %q, expected %s)", got, cur.job.SHA256)
	}
	return nil
}

// decrypt replaces the current file with its decrypted contents. Both
// armored (text) and binary PGP files are accepted. A tampered file fails
// its integrity check and is rejected.
//
// With signer_keys, the file must also be signed by one of those keys.
// The decrypted data stays in a temp file until the signature is checked,
// so nothing unverified is ever passed on.
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

	// One key ring holds our private key (to decrypt) and the partner's
	// public keys (to check the signature).
	keyring := append(append(openpgp.EntityList{}, d.Keys...), d.Signers...)
	md, err := openpgp.ReadMessage(src, keyring, nil, nil)
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
	// The signature can only be checked once all of the data is read.
	if err := checkSignature(md, d.Signers); err != nil {
		return err
	}

	cur.path = out.Name()
	cur.filename = trimPGPExt(cur.filename)
	return out.Close()
}

// checkSignature makes sure a message was signed by one of the signers.
// With no signers configured, signatures are not required.
func checkSignature(md *openpgp.MessageDetails, signers openpgp.EntityList) error {
	if len(signers) == 0 {
		return nil
	}
	if !md.IsSigned {
		return permanent("the file is not signed, but signer_keys requires a signature")
	}
	if len(signers.KeysById(md.SignedByKeyId)) == 0 {
		return permanent("the file is signed by an unknown key %X", md.SignedByKeyId)
	}
	if md.SignatureError != nil {
		return permanent("bad signature: %v", md.SignatureError)
	}
	return nil
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

// OutName returns the name a file gets after the steps run: each
// pgp_encrypt adds ".pgp". Transfers use it to look for a file the partner
// already has without encrypting it first.
func OutName(steps []*config.Step, name string) string {
	for _, s := range steps {
		if s.PGPEncrypt != nil {
			name += ".pgp"
		}
	}
	return name
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

	_, err = storage.PutFile(ctx, bucket, key, f, info.Size(), storage.PutOptions{Metadata: map[string]string{
		"sftpgate-job":    cur.job.ID,
		"sftpgate-source": cur.job.Storage + "/" + cur.job.Key,
	}})
	if err != nil {
		return err
	}
	cur.copies = append(cur.copies, Copied{Storage: c.Storage, Key: key})
	return nil
}

// WebhookPayload is the JSON body a webhook receives.
type WebhookPayload struct {
	// Event is what happened: upload, download, push or pull.
	Event string `json:"event"`
	JobID string `json:"job_id"`

	// Account and Folder are set for uploads and downloads, Transfer,
	// Remote and RemotePath for pushes and pulls.
	Account    string `json:"account"`
	Folder     string `json:"folder"`
	Transfer   string `json:"transfer,omitempty"`
	Remote     string `json:"remote,omitempty"`
	RemotePath string `json:"remote_path,omitempty"`

	Storage  string `json:"storage"`
	Key      string `json:"key"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`

	// Time is when the event happened. Uploaded holds the same time, under
	// the name older receivers expect.
	Time     time.Time `json:"time"`
	Uploaded time.Time `json:"uploaded"`

	// Copies lists the files made by copy steps that ran before this webhook.
	Copies []Copied `json:"copies"`
}

// hookEvents names the event a webhook reports for each after_* hook.
var hookEvents = map[string]string{
	config.AfterUpload:   "upload",
	config.AfterDownload: "download",
	config.AfterPush:     "push",
	config.AfterPull:     "pull",
}

// webhook posts the file's details as JSON.
//
// With a secret, the body is signed: X-Sftpgate-Signature is
// "sha256=" + hex(HMAC-SHA256(secret, body)). A job can run more than once
// after a failure, so receivers should ignore a job_id they already saw.
func (r *Runner) webhook(ctx context.Context, cur *current, w *config.Webhook) error {
	job := cur.job
	body, err := json.Marshal(WebhookPayload{
		Event:      hookEvents[job.hook()],
		JobID:      job.ID,
		Account:    job.Account,
		Folder:     job.Folder,
		Transfer:   job.Transfer,
		Remote:     job.Remote,
		RemotePath: job.RemotePath,
		Storage:    job.Storage,
		Key:        job.Key,
		Filename:   job.Filename,
		Size:       job.Size,
		SHA256:     job.SHA256,
		Time:       job.Uploaded,
		Uploaded:   job.Uploaded,
		Copies:     append([]Copied{}, cur.copies...),
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return permanent("bad webhook URL: %v", err)
	}
	// Custom headers first, so they can never replace SFTPgate's own.
	for name, value := range w.Headers {
		req.Header.Set(name, value)
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
		"transfer": cur.job.Transfer,
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

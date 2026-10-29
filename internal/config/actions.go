package config

import (
	"bytes"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// Queue holds the settings for running the actions of after_* hooks.
type Queue struct {
	Workers     int           `yaml:"workers"`
	MaxAttempts int           `yaml:"max_attempts"`
	Backoff     time.Duration `yaml:"backoff"`
}

// Step is one action. Exactly one of its fields is set.
type Step struct {
	Check      *Check      `yaml:"check"`
	PGPDecrypt *PGPDecrypt `yaml:"pgp_decrypt"`
	PGPEncrypt *PGPEncrypt `yaml:"pgp_encrypt"`
	Copy       *Copy       `yaml:"copy"`
	Webhook    *Webhook    `yaml:"webhook"`

	// Enabled false switches the action off everywhere it is used. It is
	// left out of every hook, so it doesn't run, not even for jobs already
	// queued. Unset means on.
	Enabled *bool `yaml:"enabled"`
}

// On reports whether the action is switched on.
func (s *Step) On() bool {
	return s.Enabled == nil || *s.Enabled
}

// Name returns the step's kind as written in the YAML.
func (s *Step) Name() string {
	switch {
	case s.Check != nil:
		return "check"
	case s.PGPDecrypt != nil:
		return "pgp_decrypt"
	case s.PGPEncrypt != nil:
		return "pgp_encrypt"
	case s.Copy != nil:
		return "copy"
	case s.Webhook != nil:
		return "webhook"
	}
	return ""
}

// Check rejects a file that doesn't look right. It only reads the file.
type Check struct {
	MinSize Size      `yaml:"min_size"`
	MaxSize Size      `yaml:"max_size"`
	CSV     *CSVCheck `yaml:"csv"`
}

// CSVCheck reads the file as CSV. Every row must have as many columns as
// the first one.
type CSVCheck struct {
	// Header, when set, must be exactly the first row.
	Header []string `yaml:"header"`
	// MinRows is the least number of rows after the header.
	MinRows int `yaml:"min_rows"`
	// Delimiter is one character; the default is a comma.
	Delimiter string `yaml:"delimiter"`
}

// PGPDecrypt decrypts the file with a private key.
type PGPDecrypt struct {
	Key        string `yaml:"key"`
	Passphrase string `yaml:"passphrase"`

	// SignerKeys are the partner's public keys. When set, every file must
	// be signed by one of them, or it is rejected.
	SignerKeys []string `yaml:"signer_keys"`

	// Keys is the private key, already unlocked with the passphrase.
	Keys openpgp.EntityList `yaml:"-"`

	// Signers are the parsed SignerKeys.
	Signers openpgp.EntityList `yaml:"-"`
}

// PGPEncrypt encrypts the file for one or more public keys.
type PGPEncrypt struct {
	Recipients []string `yaml:"recipients"`

	Keys openpgp.EntityList `yaml:"-"`
}

// Copy saves the file to a storage, under a key built from a template.
type Copy struct {
	Storage string `yaml:"storage"`
	Key     string `yaml:"key"`
}

// Webhook tells another service about the file.
type Webhook struct {
	URL    string `yaml:"url"`
	Secret string `yaml:"secret"`

	// Headers are sent with every call, for receivers with their own auth.
	Headers map[string]string `yaml:"headers"`
}

// The hooks a folder or a transfer can run actions from.
const (
	BeforeUpload  = "before_upload"
	AfterUpload   = "after_upload"
	AfterDownload = "after_download"
	BeforePush    = "before_push"
	AfterPush     = "after_push"
	BeforePull    = "before_pull"
	AfterPull     = "after_pull"
)

// Hooks lists, by name, the actions to run at each point of a file's way.
type Hooks struct {
	BeforeUpload  []string `yaml:"before_upload"`
	AfterUpload   []string `yaml:"after_upload"`
	AfterDownload []string `yaml:"after_download"`
	BeforePush    []string `yaml:"before_push"`
	AfterPush     []string `yaml:"after_push"`
	BeforePull    []string `yaml:"before_pull"`
	AfterPull     []string `yaml:"after_pull"`

	steps map[string][]*Step
}

// Steps returns the actions of one hook, in order.
func (h *Hooks) Steps(hook string) []*Step {
	return h.steps[hook]
}

// hookList is one hook and the action names written for it.
type hookList struct {
	hook  string
	names []string
}

func (h *Hooks) lists() []hookList {
	return []hookList{
		{BeforeUpload, h.BeforeUpload}, {AfterUpload, h.AfterUpload}, {AfterDownload, h.AfterDownload},
		{BeforePush, h.BeforePush}, {AfterPush, h.AfterPush},
		{BeforePull, h.BeforePull}, {AfterPull, h.AfterPull},
	}
}

// beforeKinds lists the actions a "before" hook may run. Something is
// waiting on these (a partner, or a transfer about to send), so they only
// check the file, or prepare it to be sent. Every other action runs from an
// after_* hook, in the background, with retries.
var beforeKinds = map[string]map[string]bool{
	BeforeUpload: {"check": true},
	BeforePull:   {"check": true},
	BeforePush:   {"check": true, "pgp_encrypt": true},
}

// templateVarRe matches {name} placeholders in a copy key.
var templateVarRe = regexp.MustCompile(`\{([^}]*)\}`)

// TemplateVars are the placeholders a copy key may use.
var TemplateVars = map[string]bool{
	"account": true, "folder": true, "transfer": true, "date": true, "time": true, "filename": true, "job_id": true,
}

// RenderKey fills the {placeholders} in a copy key template.
func RenderKey(template string, vars map[string]string) string {
	return templateVarRe.ReplaceAllStringFunc(template, func(m string) string {
		return vars[m[1:len(m)-1]]
	})
}

var actionNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// actions checks every named action once, where it is defined.
func (c *checker) actions(cfg *Config) {
	for _, name := range slices.Sorted(maps.Keys(cfg.Actions)) {
		p := "actions." + name
		if !actionNameRe.MatchString(name) {
			c.fail(p, "name must match %s", actionNameRe)
		}
		if cfg.Actions[name] == nil {
			c.fail(p, "needs one of check, pgp_decrypt, pgp_encrypt, copy, webhook")
			continue
		}
		c.step(p, cfg.Actions[name], cfg.Storages)
	}
}

// hooks resolves the action names of each hook. allowed says which hooks
// make sense here, for example after_download only on a folder.
func (c *checker) hooks(p string, h *Hooks, allowed []string, cfg *Config) {
	h.steps = map[string][]*Step{}
	for _, l := range h.lists() {
		hook, refs := l.hook, l.names
		if len(refs) == 0 {
			continue
		}
		hp := p + ".hooks." + hook
		if !slices.Contains(allowed, hook) {
			c.fail(hp, "not available here (use %s)", strings.Join(allowed, ", "))
			continue
		}
		for i, name := range refs {
			step := cfg.Actions[name]
			if step == nil {
				c.fail(fmt.Sprintf("%s[%d]", hp, i), "unknown action %q (define it under actions:)", name)
				continue
			}
			if kinds := beforeKinds[hook]; kinds != nil && !kinds[step.Name()] {
				c.fail(fmt.Sprintf("%s[%d]", hp, i), "%s can't run %s (%s is a %s); before hooks only run %s",
					hook, name, name, step.Name(), strings.Join(slices.Sorted(maps.Keys(kinds)), ", "))
				continue
			}
			if step.On() {
				h.steps[hook] = append(h.steps[hook], step)
			}
		}
	}
}

// onUpload checks the older on_upload steps of a folder and turns them into
// its after_upload hook.
func (c *checker) onUpload(p string, f *Folder, storages map[string]string) {
	if len(f.OnUpload) == 0 {
		return
	}
	if len(f.Hooks.AfterUpload) > 0 {
		c.fail(p+".on_upload", "use hooks.after_upload or on_upload, not both")
		return
	}
	for i, s := range f.OnUpload {
		c.step(fmt.Sprintf("%s.on_upload[%d]", p, i), s, storages)
		if s.On() {
			f.Hooks.steps[AfterUpload] = append(f.Hooks.steps[AfterUpload], s)
		}
	}
}

// step checks one action. p is where it is written in the file.
func (c *checker) step(p string, s *Step, storages map[string]string) {
	set := 0
	for _, ok := range []bool{s.Check != nil, s.PGPDecrypt != nil, s.PGPEncrypt != nil, s.Copy != nil, s.Webhook != nil} {
		if ok {
			set++
		}
	}
	if set != 1 {
		c.fail(p, "each action needs exactly one of check, pgp_decrypt, pgp_encrypt, copy, webhook")
		return
	}

	// Pausing encryption would quietly send files in the clear. Removing
	// it from the hook is a change everyone can see in review.
	if !s.On() && s.PGPEncrypt != nil {
		c.fail(p+".enabled", "pgp_encrypt can't be switched off; remove it from the hooks instead")
	}

	sp := p + "." + s.Name()
	switch {
	case s.Check != nil:
		c.check(sp, s.Check)
	case s.PGPDecrypt != nil:
		c.pgpDecrypt(sp, s.PGPDecrypt)
	case s.PGPEncrypt != nil:
		c.pgpEncrypt(sp, s.PGPEncrypt)
	case s.Copy != nil:
		c.copyStep(sp, s.Copy, storages)
	case s.Webhook != nil:
		c.webhook(sp, s.Webhook)
	}
}

func (c *checker) check(p string, ch *Check) {
	if ch.MinSize == 0 && ch.MaxSize == 0 && ch.CSV == nil {
		c.fail(p, "needs at least one of min_size, max_size, csv")
	}
	if ch.MinSize < 0 || ch.MaxSize < 0 {
		c.fail(p, "min_size and max_size must be 0 or more")
	}
	if ch.MaxSize > 0 && ch.MinSize > ch.MaxSize {
		c.fail(p, "min_size is bigger than max_size")
	}
	if csv := ch.CSV; csv != nil {
		if csv.MinRows < 0 {
			c.fail(p+".csv.min_rows", "must be 0 or more")
		}
		if csv.Delimiter != "" && utf8.RuneCountInString(csv.Delimiter) != 1 {
			c.fail(p+".csv.delimiter", "must be one character")
		}
		for i, h := range csv.Header {
			if h == "" {
				c.fail(fmt.Sprintf("%s.csv.header[%d]", p, i), "can't be empty")
			}
		}
	}
}

func (c *checker) pgpDecrypt(p string, d *PGPDecrypt) {
	c.secret(p+".key", d.Key, true)
	c.secret(p+".passphrase", d.Passphrase, false)
	d.Signers = c.publicKeys(p+".signer_keys", d.SignerKeys)
	if c.unresolved(p + ".key") {
		return
	}

	keys, err := openpgp.ReadArmoredKeyRing(bytes.NewBufferString(d.Key))
	if err != nil {
		c.fail(p+".key", "not an armored PGP private key: %v", err)
		return
	}
	for _, e := range keys {
		if e.PrivateKey == nil {
			c.fail(p+".key", "this is a public key; decrypting needs the private key")
			return
		}
		if c.unresolved(p + ".passphrase") {
			continue
		}
		if err := e.DecryptPrivateKeys([]byte(d.Passphrase)); err != nil {
			c.fail(p+".passphrase", "cannot unlock the private key: %v", err)
			return
		}
	}
	d.Keys = keys
}

func (c *checker) pgpEncrypt(p string, e *PGPEncrypt) {
	if len(e.Recipients) == 0 {
		c.fail(p+".recipients", "at least one public key is required")
	}
	e.Keys = c.publicKeys(p+".recipients", e.Recipients)
	// Found now, not when a transfer runs at 10:00: a key that can only sign,
	// or has expired, can't be encrypted for.
	for _, k := range e.Keys {
		if _, ok := k.EncryptionKey(time.Now()); !ok {
			c.fail(p+".recipients", "key %X has no usable encryption key (expired, revoked, or sign-only)", k.PrimaryKey.KeyId)
		}
	}
}

// publicKeys parses a list of armored PGP public keys.
func (c *checker) publicKeys(p string, armored []string) openpgp.EntityList {
	var all openpgp.EntityList
	for i, a := range armored {
		kp := fmt.Sprintf("%s[%d]", p, i)
		if c.unresolved(kp) {
			continue
		}
		keys, err := openpgp.ReadArmoredKeyRing(bytes.NewBufferString(a))
		if err != nil {
			c.fail(kp, "not an armored PGP public key: %v", err)
			continue
		}
		all = append(all, keys...)
	}
	return all
}

func (c *checker) copyStep(p string, cp *Copy, storages map[string]string) {
	if _, ok := storages[cp.Storage]; !ok {
		c.fail(p+".storage", "unknown storage %q", cp.Storage)
	}
	if cp.Key == "" {
		c.fail(p+".key", "a key template is required, e.g. archive/{date}/{filename}")
	}
	for _, m := range templateVarRe.FindAllStringSubmatch(cp.Key, -1) {
		if !TemplateVars[m[1]] {
			c.fail(p+".key", "unknown placeholder {%s} (use %s)", m[1], strings.Join(slices.Sorted(maps.Keys(TemplateVars)), ", "))
		}
	}
}

func (c *checker) webhook(p string, w *Webhook) {
	c.secret(p+".secret", w.Secret, false)
	if !c.unresolved(p + ".url") {
		u, err := url.Parse(w.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			c.fail(p+".url", "must be an http or https URL")
		}
	}

	for name := range w.Headers {
		hp := p + ".headers." + name
		lower := strings.ToLower(name)
		switch {
		case name == "" || strings.ContainsAny(name, " :\t\r\n"):
			c.fail(hp, "not a valid header name")
		case lower == "content-type" || lower == "user-agent" || strings.HasPrefix(lower, "x-sftpgate-"):
			c.fail(hp, "set by SFTPgate, can't be changed")
		case sensitiveHeader(lower):
			c.secret(hp, w.Headers[name], true)
		}
	}
}

// sensitiveHeader reports whether a header usually carries a secret, which
// must then come from a reference.
func sensitiveHeader(lower string) bool {
	for _, s := range []string{"auth", "token", "key", "secret", "cookie"} {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// secret checks that a secret value comes from a reference, never written
// into the file itself. An empty optional secret is fine.
func (c *checker) secret(p, value string, required bool) {
	isRef := c.refs[p]
	switch {
	case value == "" && !isRef && required:
		c.fail(p, "is required")
	case value != "" && !isRef:
		c.fail(p, "inline secret not allowed, use ${env:NAME}, ${file:/path} or ${secret:url}")
	}
}

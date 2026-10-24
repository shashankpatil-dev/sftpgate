package config

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// Actions holds the settings for running post-upload actions.
type Actions struct {
	Workers     int           `yaml:"workers"`
	MaxAttempts int           `yaml:"max_attempts"`
	Backoff     time.Duration `yaml:"backoff"`
}

// Step is one post-upload action. Exactly one of its fields is set.
type Step struct {
	PGPDecrypt *PGPDecrypt `yaml:"pgp_decrypt"`
	PGPEncrypt *PGPEncrypt `yaml:"pgp_encrypt"`
	Copy       *Copy       `yaml:"copy"`
	Webhook    *Webhook    `yaml:"webhook"`
}

// Name returns the step's name as written in the YAML.
func (s *Step) Name() string {
	switch {
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

// PGPDecrypt decrypts the file with a private key.
type PGPDecrypt struct {
	Key        string `yaml:"key"`
	Passphrase string `yaml:"passphrase"`

	// Keys is the private key, already unlocked with the passphrase.
	Keys openpgp.EntityList `yaml:"-"`
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

// Webhook tells another service that a file arrived.
type Webhook struct {
	URL    string `yaml:"url"`
	Secret string `yaml:"secret"`
}

// templateVarRe matches {name} placeholders in a copy key.
var templateVarRe = regexp.MustCompile(`\{([^}]*)\}`)

// TemplateVars are the placeholders a copy key may use.
var TemplateVars = map[string]bool{
	"account": true, "folder": true, "date": true, "time": true, "filename": true, "job_id": true,
}

// RenderKey fills the {placeholders} in a copy key template.
func RenderKey(template string, vars map[string]string) string {
	return templateVarRe.ReplaceAllStringFunc(template, func(m string) string {
		return vars[m[1:len(m)-1]]
	})
}

func (c *checker) steps(p string, steps []*Step, storages map[string]string) {
	for i, s := range steps {
		sp := fmt.Sprintf("%s.on_upload[%d]", p, i)

		set := 0
		for _, ok := range []bool{s.PGPDecrypt != nil, s.PGPEncrypt != nil, s.Copy != nil, s.Webhook != nil} {
			if ok {
				set++
			}
		}
		if set != 1 {
			c.fail(sp, "each step needs exactly one of pgp_decrypt, pgp_encrypt, copy, webhook")
			continue
		}

		sp += "." + s.Name()
		switch {
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
}

func (c *checker) pgpDecrypt(p string, d *PGPDecrypt) {
	c.secret(p+".key", d.Key, true)
	c.secret(p+".passphrase", d.Passphrase, false)
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
	for i, r := range e.Recipients {
		rp := fmt.Sprintf("%s.recipients[%d]", p, i)
		if c.unresolved(rp) {
			continue
		}
		keys, err := openpgp.ReadArmoredKeyRing(bytes.NewBufferString(r))
		if err != nil {
			c.fail(rp, "not an armored PGP public key: %v", err)
			continue
		}
		e.Keys = append(e.Keys, keys...)
	}
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
			c.fail(p+".key", "unknown placeholder {%s} (use account, folder, date, time, filename, job_id)", m[1])
		}
	}
}

func (c *checker) webhook(p string, w *Webhook) {
	c.secret(p+".secret", w.Secret, false)
	if c.unresolved(p + ".url") {
		return
	}
	u, err := url.Parse(w.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		c.fail(p+".url", "must be an http or https URL")
	}
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

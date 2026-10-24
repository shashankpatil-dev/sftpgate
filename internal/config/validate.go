package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

var (
	accountNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	storageNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)
	folderPathRe  = regexp.MustCompile(`^/[a-zA-Z0-9._-]+$`)
	schemes       = map[string]bool{"gs": true, "s3": true, "azblob": true, "file": true, "mem": true}
)

// checker collects every problem found in a config, so the user can fix
// them all in one go instead of one per run.
type checker struct {
	errs    []error
	refs    map[string]bool
	offline bool
}

func (c *checker) fail(path, format string, a ...any) {
	c.errs = append(c.errs, fmt.Errorf("%s: %s", path, fmt.Sprintf(format, a...)))
}

// unresolved reports whether the value at path is a reference that was
// skipped offline, which means its real value can't be checked.
func (c *checker) unresolved(path string) bool {
	return c.offline && c.refs[path]
}

// validate checks the whole config and fills in the fields that are worked
// out from it (parsed keys, IP ranges, permission bits, fingerprints).
func validate(cfg *Config, refs map[string]bool, opt Options) error {
	c := &checker{refs: refs, offline: opt.Offline}

	if cfg.Version != 1 {
		c.fail("version", "must be 1")
	}
	c.server(cfg)
	c.storages(cfg.Storages)

	cfg.byName = map[string]*Account{}
	for i, a := range cfg.Accounts {
		c.account(fmt.Sprintf("accounts[%d]", i), a, cfg)
	}

	return joinErrs(c.errs)
}

func (c *checker) server(cfg *Config) {
	s := cfg.Server

	if len(s.HostKeys) == 0 {
		c.fail("server.host_keys", "at least one host key file is required")
	}
	if !c.offline {
		for i, p := range s.HostKeys {
			signer, err := loadHostKey(p)
			if err != nil {
				c.fail(fmt.Sprintf("server.host_keys[%d]", i), "%v", err)
				continue
			}
			cfg.HostKeys = append(cfg.HostKeys, signer)
		}
	}

	cfg.GlobalIPs = c.prefixes("server.allow_ips", s.AllowIPs)

	if s.MaxAuthTries < 1 || s.Throttle.MaxFailures < 1 {
		c.fail("server", "max_auth_tries and throttle.max_failures must be at least 1")
	}

	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		c.fail("server.timezone", "unknown time zone %q (use e.g. UTC or Asia/Kolkata)", s.Timezone)
	}
	cfg.Server.Location = loc

	if cfg.Actions.Workers < 1 || cfg.Actions.MaxAttempts < 1 || cfg.Actions.Backoff <= 0 {
		c.fail("actions", "workers, max_attempts and backoff must all be above zero")
	}
}

func (c *checker) storages(storages map[string]string) {
	// Sort the names so errors always come out in the same order.
	names := make([]string, 0, len(storages))
	for name := range storages {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		p := "storages." + name
		if !storageNameRe.MatchString(name) {
			c.fail(p, "name must match %s", storageNameRe)
		}
		if c.unresolved(p) {
			continue
		}
		scheme, _, ok := strings.Cut(storages[name], "://")
		if !ok || !schemes[scheme] {
			c.fail(p, "unsupported storage URL %q (gs://, s3://, azblob://, file://, mem://)", storages[name])
		}
	}
}

func (c *checker) account(p string, a *Account, cfg *Config) {
	if !accountNameRe.MatchString(a.Name) {
		c.fail(p+".name", "must match %s", accountNameRe)
	}
	if cfg.byName[a.Name] != nil {
		c.fail(p+".name", "duplicate account %q", a.Name)
	}
	cfg.byName[a.Name] = a

	c.auth(p+".auth", a)
	a.IPs = c.prefixes(p+".allow_ips", a.AllowIPs)

	if a.MaxSessions < 0 {
		c.fail(p+".max_sessions", "must be 0 or more")
	}
	if len(a.Folders) == 0 {
		c.fail(p+".folders", "at least one folder is required")
	}

	seen := map[string]bool{}
	for j, f := range a.Folders {
		fp := fmt.Sprintf("%s.folders[%d]", p, j)
		if seen[f.Path] {
			c.fail(fp+".path", "duplicate folder %q", f.Path)
		}
		seen[f.Path] = true
		c.folder(fp, f, cfg.Storages)
	}

	// Worked out last, after the folder prefixes have been tidied up.
	a.Fingerprint = fingerprint(a, cfg.Storages)
}

func (c *checker) auth(p string, a *Account) {
	au := a.Auth
	if au.Mode != ModeAny && au.Mode != ModeAll {
		c.fail(p+".mode", "must be any or all")
	}

	for i, k := range au.PublicKeys {
		kp := fmt.Sprintf("%s.public_keys[%d]", p, i)
		if c.unresolved(kp) {
			continue
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k))
		if err != nil {
			c.fail(kp, "invalid public key: %v", err)
			continue
		}
		if key.Type() == "ssh-dss" {
			c.fail(kp, "ssh-dss keys are not allowed")
			continue
		}
		a.Keys = append(a.Keys, key.Marshal())
	}

	// The password hash is a secret, so it must come from a reference,
	// never be written into the file itself.
	hp := p + ".password_hash"
	isRef := c.refs[hp]
	hasPassword := au.PasswordHash != "" || isRef
	switch {
	case hasPassword && !isRef:
		c.fail(hp, "inline secret not allowed, use ${env:NAME}, ${file:/path} or ${secret:url}")
	case hasPassword && !c.unresolved(hp):
		cost, err := bcrypt.Cost([]byte(au.PasswordHash))
		if err != nil || cost < 10 {
			c.fail(hp, "must be a bcrypt hash with cost 10 or more (see sftpgate hash-password); possible plaintext password")
		}
	}

	hasKey := len(au.PublicKeys) > 0
	switch {
	case au.Mode == ModeAll && (!hasKey || !hasPassword):
		c.fail(p, "mode all requires both public_keys and password_hash")
	case au.Mode == ModeAny && !hasKey && !hasPassword:
		c.fail(p, "needs public_keys or password_hash")
	}
}

func (c *checker) folder(p string, f *Folder, storages map[string]string) {
	if !folderPathRe.MatchString(f.Path) {
		c.fail(p+".path", "must be a single segment like /inbound")
	}
	if _, ok := storages[f.Storage]; !ok {
		c.fail(p+".storage", "unknown storage %q", f.Storage)
	}

	// The prefix must stay inside the bucket: no leading slash, no . or ..
	wrapped := "/" + f.Prefix + "/"
	if strings.HasPrefix(f.Prefix, "/") || strings.Contains(wrapped, "/../") || strings.Contains(wrapped, "/./") {
		c.fail(p+".prefix", "must be relative with no . or .. segments")
	}
	if f.Prefix != "" && !strings.HasSuffix(f.Prefix, "/") {
		f.Prefix += "/"
	}

	if len(f.Perms) == 0 {
		c.fail(p+".perms", "at least one permission is required")
	}
	for _, name := range f.Perms {
		bit, ok := permNames[name]
		if !ok {
			c.fail(p+".perms", "unknown permission %q", name)
		}
		f.Perm |= bit
	}

	for i, pat := range f.Patterns {
		if _, err := path.Match(pat, "x"); err != nil {
			c.fail(fmt.Sprintf("%s.patterns[%d]", p, i), "bad pattern %q", pat)
		}
	}
	if f.MaxFileSize < 0 {
		c.fail(p+".max_file_size", "must be 0 or more")
	}

	c.steps(p, f.OnUpload, storages)
}

// prefixes parses a list of IPs and CIDR ranges. A single IP becomes a
// range that holds just that IP.
func (c *checker) prefixes(p string, ips []string) []netip.Prefix {
	var out []netip.Prefix
	for i, s := range ips {
		ip := fmt.Sprintf("%s[%d]", p, i)
		if c.unresolved(ip) {
			continue
		}
		if pre, err := netip.ParsePrefix(s); err == nil {
			out = append(out, pre.Masked())
			continue
		}
		addr, err := netip.ParseAddr(s)
		if err != nil {
			c.fail(ip, "invalid IP or CIDR %q", s)
			continue
		}
		addr = addr.Unmap()
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out
}

func loadHostKey(p string) (ssh.Signer, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(b)
}

// fingerprint hashes an account together with the URLs of the storages it
// uses. Comments and other accounts don't affect it.
func fingerprint(a *Account, storages map[string]string) string {
	h := sha256.New()
	b, _ := yaml.Marshal(a)
	h.Write(b)

	var used []string
	for _, f := range a.Folders {
		used = append(used, f.Storage+"="+storages[f.Storage])
	}
	sort.Strings(used)
	h.Write([]byte(strings.Join(used, "\n")))

	return hex.EncodeToString(h.Sum(nil))
}

func joinErrs(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

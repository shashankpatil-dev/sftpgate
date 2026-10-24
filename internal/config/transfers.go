package config

import (
	"fmt"
	"net"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/robfig/cron/v3"
	"golang.org/x/crypto/ssh"
)

// Remote is another SFTP server that scheduled transfers connect to.
type Remote struct {
	Addr       string `yaml:"addr"`
	User       string `yaml:"user"`
	HostKey    string `yaml:"host_key"`
	PrivateKey string `yaml:"private_key"`
	Password   string `yaml:"password"`

	// Signer and Host are the parsed private key and pinned host key.
	Signer ssh.Signer    `yaml:"-"`
	Host   ssh.PublicKey `yaml:"-"`
}

// Transfer moves files on a schedule, either from a remote into a storage
// (pull) or from a storage to a remote (push). Exactly one of Pull and Push
// is set.
type Transfer struct {
	Name     string `yaml:"name"`
	Schedule string `yaml:"schedule"`
	Pull     *Pull  `yaml:"pull"`
	Push     *Push  `yaml:"push"`
}

// Pull copies new files from a remote folder into a storage.
type Pull struct {
	From struct {
		Remote  string `yaml:"remote"`
		Path    string `yaml:"path"`
		Pattern string `yaml:"pattern"`
	} `yaml:"from"`
	To struct {
		Storage string `yaml:"storage"`
		Key     string `yaml:"key"`
	} `yaml:"to"`

	// DeleteAfter removes the remote file once it is safely stored.
	DeleteAfter bool `yaml:"delete_after"`
}

// Push sends files from a storage prefix to a remote folder.
type Push struct {
	From struct {
		Storage string `yaml:"storage"`
		Prefix  string `yaml:"prefix"`
		Pattern string `yaml:"pattern"`
	} `yaml:"from"`
	To struct {
		Remote string `yaml:"remote"`
		Path   string `yaml:"path"`
	} `yaml:"to"`

	// After says what happens to a file once it is sent: "keep" (and skip
	// files the remote already has), "delete", or "move:<prefix>".
	After string `yaml:"after"`
}

// MovePrefix returns the prefix of an "after: move:<prefix>" setting.
func (p *Push) MovePrefix() (string, bool) {
	return strings.CutPrefix(p.After, "move:")
}

// TransferKeyVars are the placeholders a pull key may use.
var TransferKeyVars = map[string]bool{"name": true, "date": true, "time": true, "filename": true}

var transferNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func (c *checker) remotes(remotes map[string]*Remote) {
	// Sort the names so errors always come out in the same order.
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		r, p := remotes[name], "remotes."+name
		if !storageNameRe.MatchString(name) {
			c.fail(p, "name must match %s", storageNameRe)
		}
		if _, _, err := net.SplitHostPort(r.Addr); err != nil {
			c.fail(p+".addr", "must be host:port, like sftp.bank.example:22")
		}
		if r.User == "" {
			c.fail(p+".user", "is required")
		}

		// The remote's host key must be pinned: without it anyone in the
		// middle could pose as the partner's server.
		if !c.unresolved(p + ".host_key") {
			host, _, _, _, err := ssh.ParseAuthorizedKey([]byte(r.HostKey))
			if err != nil {
				c.fail(p+".host_key", "must be the remote's public host key, like \"ssh-ed25519 AAAA...\"")
			}
			r.Host = host
		}

		c.secret(p+".private_key", r.PrivateKey, false)
		c.secret(p+".password", r.Password, false)
		if r.PrivateKey == "" && r.Password == "" && !c.refs[p+".private_key"] && !c.refs[p+".password"] {
			c.fail(p, "needs private_key or password")
		}
		if r.PrivateKey != "" && !c.unresolved(p+".private_key") {
			signer, err := ssh.ParsePrivateKey([]byte(r.PrivateKey))
			if err != nil {
				c.fail(p+".private_key", "cannot read the private key: %v", err)
			}
			r.Signer = signer
		}
	}
}

func (c *checker) transfers(cfg *Config) {
	seen := map[string]bool{}
	for i, t := range cfg.Transfers {
		p := fmt.Sprintf("transfers[%d]", i)
		if !transferNameRe.MatchString(t.Name) {
			c.fail(p+".name", "must match %s", transferNameRe)
		}
		if seen[t.Name] {
			c.fail(p+".name", "duplicate transfer %q", t.Name)
		}
		seen[t.Name] = true

		if _, err := cron.ParseStandard(t.Schedule); err != nil {
			c.fail(p+".schedule", "bad schedule %q: %v (use cron like \"0 6 * * *\")", t.Schedule, err)
		}

		switch {
		case (t.Pull == nil) == (t.Push == nil):
			c.fail(p, "needs exactly one of pull or push")
		case t.Pull != nil:
			c.pull(p+".pull", t.Pull, cfg)
		default:
			c.push(p+".push", t.Push, cfg)
		}
	}
}

func (c *checker) pull(p string, pl *Pull, cfg *Config) {
	c.remoteRef(p+".from.remote", pl.From.Remote, cfg)
	c.remotePath(p+".from.path", pl.From.Path)
	c.pattern(p+".from.pattern", pl.From.Pattern)
	c.storageRef(p+".to.storage", pl.To.Storage, cfg)
	if pl.To.Key == "" {
		c.fail(p+".to.key", "a key template is required, e.g. bank/{date}/{filename}")
	}
	for _, m := range templateVarRe.FindAllStringSubmatch(pl.To.Key, -1) {
		if !TransferKeyVars[m[1]] {
			c.fail(p+".to.key", "unknown placeholder {%s} (use name, date, time, filename)", m[1])
		}
	}
	if !strings.Contains(pl.To.Key, "{filename}") {
		c.fail(p+".to.key", "must contain {filename}, or every file would overwrite the last")
	}
}

func (c *checker) push(p string, ps *Push, cfg *Config) {
	c.storageRef(p+".from.storage", ps.From.Storage, cfg)
	c.relativePrefix(p+".from.prefix", ps.From.Prefix)
	c.pattern(p+".from.pattern", ps.From.Pattern)
	c.remoteRef(p+".to.remote", ps.To.Remote, cfg)
	c.remotePath(p+".to.path", ps.To.Path)

	switch move, isMove := ps.MovePrefix(); {
	case ps.After == "":
		ps.After = "keep"
	case isMove:
		c.relativePrefix(p+".after", move)
		if move == "" || move == ps.From.Prefix {
			c.fail(p+".after", "move needs a prefix different from from.prefix")
		}
	case ps.After != "keep" && ps.After != "delete":
		c.fail(p+".after", "must be keep, delete or move:<prefix>")
	}
}

func (c *checker) remoteRef(p, name string, cfg *Config) {
	if cfg.Remotes[name] == nil {
		c.fail(p, "unknown remote %q", name)
	}
}

func (c *checker) storageRef(p, name string, cfg *Config) {
	if _, ok := cfg.Storages[name]; !ok {
		c.fail(p, "unknown storage %q", name)
	}
}

func (c *checker) remotePath(p, value string) {
	if !path.IsAbs(value) || path.Clean(value) != value {
		c.fail(p, "must be a clean absolute path, like /out")
	}
}

func (c *checker) relativePrefix(p, value string) {
	wrapped := "/" + value + "/"
	if strings.HasPrefix(value, "/") || strings.Contains(wrapped, "/../") || strings.Contains(wrapped, "/./") {
		c.fail(p, "must be relative with no . or .. segments")
	}
}

func (c *checker) pattern(p, value string) {
	if value == "" {
		return
	}
	if _, err := path.Match(value, "x"); err != nil {
		c.fail(p, "bad pattern %q", value)
	}
}

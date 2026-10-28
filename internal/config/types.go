// Package config reads the sftpgate.yaml file, checks it, and turns it into
// a Config that the rest of SFTPgate uses.
package config

import (
	"net/netip"
	"time"

	"golang.org/x/crypto/ssh"
)

// Config is one loaded and checked version of sftpgate.yaml.
// It is never changed after loading; a reload builds a new one.
type Config struct {
	Version  int               `yaml:"version"`
	Server   Server            `yaml:"server"`
	HTTP     HTTP              `yaml:"http"`
	Storages map[string]string `yaml:"storages"`
	Accounts []*Account        `yaml:"accounts"`

	// Actions are named steps (check, PGP, copy, webhook) that folders and
	// transfers run from their hooks.
	Actions map[string]*Step `yaml:"actions"`

	Remotes   map[string]*Remote `yaml:"remotes"`
	Transfers []*Transfer        `yaml:"transfers"`

	// Hash is the sha256 of the file, used to spot changes and shown in metrics.
	Hash string `yaml:"-"`

	// Raw is the file exactly as written. Secrets in it are only ${...}
	// references, so it is safe to display.
	Raw []byte `yaml:"-"`

	GlobalIPs []netip.Prefix `yaml:"-"`

	// HostKeys stays empty when the file is checked offline.
	HostKeys []ssh.Signer `yaml:"-"`

	byName map[string]*Account
}

// Server holds the SFTP listener settings.
type Server struct {
	Listen       string        `yaml:"listen"`
	HostKeys     []string      `yaml:"host_keys"`
	AllowIPs     []string      `yaml:"allow_ips"`
	Banner       string        `yaml:"banner"`
	IdleTimeout  time.Duration `yaml:"idle_timeout"`
	MaxAuthTries int           `yaml:"max_auth_tries"`

	// MaxConnections caps open connections across all accounts, so a flood
	// of logins can't use up the server's memory.
	MaxConnections int           `yaml:"max_connections"`
	SpoolDir       string        `yaml:"spool_dir"`
	MinSpoolFree   Size          `yaml:"min_spool_free"`
	ReloadInterval time.Duration `yaml:"reload_interval"`
	Throttle       Throttle      `yaml:"throttle"`

	// Queue says how the actions of after_* hooks run in the background.
	Queue Queue `yaml:"queue"`

	// Timezone is used for {date} and {time} in copy keys.
	Timezone string         `yaml:"timezone"`
	Location *time.Location `yaml:"-"`
}

// Throttle blocks an IP for a while after too many failed logins.
type Throttle struct {
	MaxFailures int           `yaml:"max_failures"`
	Window      time.Duration `yaml:"window"`
	Block       time.Duration `yaml:"block"`
}

// HTTP holds the settings for the metrics, health, API and UI endpoint.
type HTTP struct {
	Listen string `yaml:"listen"`

	// Tokens open the read-only API and UI. With no tokens they are off.
	Tokens []string `yaml:"tokens"`
}

// Account is one SFTP login: a vendor, a partner or an internal service.
type Account struct {
	Name        string    `yaml:"name"`
	Description string    `yaml:"description"`
	Auth        Auth      `yaml:"auth"`
	AllowIPs    []string  `yaml:"allow_ips"`
	MaxSessions int       `yaml:"max_sessions"`
	Folders     []*Folder `yaml:"folders"`

	// Keys are the account's public keys in SSH wire format, ready to compare.
	Keys [][]byte       `yaml:"-"`
	IPs  []netip.Prefix `yaml:"-"`

	// Fingerprint changes whenever anything about this account changes.
	// On reload, sessions whose fingerprint changed are closed.
	Fingerprint string `yaml:"-"`
}

// Auth says how an account logs in.
type Auth struct {
	Mode         string   `yaml:"mode"`
	PublicKeys   []string `yaml:"public_keys"`
	PasswordHash string   `yaml:"password_hash"`
}

// Login modes: "any" means key OR password, "all" means key AND password.
const (
	ModeAny = "any"
	ModeAll = "all"
)

// Folder is one top-level folder an account sees, like /inbound.
// Everything inside it lives in a bucket under Prefix.
type Folder struct {
	Path        string   `yaml:"path"`
	Storage     string   `yaml:"storage"`
	Prefix      string   `yaml:"prefix"`
	Perms       []string `yaml:"perms"`
	Patterns    []string `yaml:"patterns"`
	MaxFileSize Size     `yaml:"max_file_size"`

	Hooks Hooks `yaml:"hooks"`

	// OnUpload is the older way to write hooks.after_upload, with the steps
	// written in place. It still works; a folder can't use both.
	OnUpload []*Step `yaml:"on_upload"`

	Perm PermSet `yaml:"-"`
}

// PermSet is a set of folder permissions stored as bits.
type PermSet uint8

// The folder permissions an account can be given.
const (
	PermList PermSet = 1 << iota
	PermDownload
	PermUpload
	PermOverwrite
	PermDelete
	PermRename
	PermMkdir
)

var permNames = map[string]PermSet{
	"list":      PermList,
	"download":  PermDownload,
	"upload":    PermUpload,
	"overwrite": PermOverwrite,
	"delete":    PermDelete,
	"rename":    PermRename,
	"mkdir":     PermMkdir,
}

// Has reports whether every permission in p is in the set.
func (s PermSet) Has(p PermSet) bool {
	return s&p == p
}

// Account finds an account by its SSH username. It returns nil if there is none.
func (c *Config) Account(name string) *Account {
	return c.byName[name]
}

// Transfer finds a transfer by name. It returns nil if there is none.
func (c *Config) Transfer(name string) *Transfer {
	for _, t := range c.Transfers {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// Folder finds the folder mounted at path, like "/inbound". It returns nil if there is none.
func (a *Account) Folder(path string) *Folder {
	for _, f := range a.Folders {
		if f.Path == path {
			return f
		}
	}
	return nil
}

// AllowsIP reports whether ip is inside one of the prefixes.
// An empty list means every IP is allowed.
func AllowsIP(prefixes []netip.Prefix, ip netip.Addr) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

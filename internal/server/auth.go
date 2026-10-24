package server

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"net/netip"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"

	"github.com/shashankpatil-dev/sftpgate/internal/config"
)

// Keys stored in ssh.Permissions.Extensions after a successful login.
const (
	extAccount = "account"
	extMethod  = "method"
	extKeyFP   = "key_fp"
)

var errAuth = errors.New("authentication failed")

// dummyHash is compared against when the user doesn't exist, so a wrong
// username takes as long as a wrong password and can't be told apart.
var dummyHash = func() []byte {
	pw := make([]byte, 32)
	_, _ = rand.Read(pw)
	h, _ := bcrypt.GenerateFromPassword(pw, bcrypt.DefaultCost)
	return h
}()

// authenticator checks logins for one connection.
type authenticator struct {
	cfg *config.Config
	ip  netip.Addr

	// user is the last username the client tried, kept for the audit log.
	user string
}

// account returns the account for user, but only if this IP may use it.
func (a *authenticator) account(user string) *config.Account {
	acc := a.cfg.Account(user)
	if acc == nil || !config.AllowsIP(acc.IPs, a.ip) {
		return nil
	}
	return acc
}

// sshConfig builds the SSH server settings for this connection.
func (a *authenticator) sshConfig() *ssh.ServerConfig {
	sc := &ssh.ServerConfig{
		MaxAuthTries:              a.cfg.Server.MaxAuthTries,
		PublicKeyCallback:         a.publicKey,
		VerifiedPublicKeyCallback: a.verifiedPublicKey,
		PasswordCallback:          a.password,
		AuthLogCallback:           a.logAttempt,
		ServerVersion:             "SSH-2.0-SFTPgate",
	}
	if banner := a.cfg.Server.Banner; banner != "" {
		sc.BannerCallback = func(ssh.ConnMetadata) string { return banner + "\n" }
	}
	for _, k := range a.cfg.HostKeys {
		sc.AddHostKey(k)
	}
	return sc
}

// logAttempt remembers the username of each login attempt. The username
// comes from the client, so it is cut short to keep log lines small.
func (a *authenticator) logAttempt(meta ssh.ConnMetadata, _ string, _ error) {
	a.user = meta.User()
	if len(a.user) > 64 {
		a.user = a.user[:64]
	}
}

// publicKey says whether a key may be used for this user. The client hasn't
// proven it owns the key yet; that happens before verifiedPublicKey runs.
func (a *authenticator) publicKey(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	acc := a.account(meta.User())
	if acc == nil || !hasKey(acc, key.Marshal()) {
		return nil, errAuth
	}
	return &ssh.Permissions{Extensions: map[string]string{
		extAccount: acc.Name,
		extMethod:  "publickey",
		extKeyFP:   ssh.FingerprintSHA256(key),
	}}, nil
}

// verifiedPublicKey runs once the client has proven it owns the key.
// For accounts with mode "all" it asks for the password as a second step.
func (a *authenticator) verifiedPublicKey(_ ssh.ConnMetadata, _ ssh.PublicKey, perms *ssh.Permissions, _ string) (*ssh.Permissions, error) {
	acc := a.cfg.Account(perms.Extensions[extAccount])
	if acc.Auth.Mode != config.ModeAll {
		return perms, nil
	}

	keyFP := perms.Extensions[extKeyFP]
	secondStep := func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
		if bcrypt.CompareHashAndPassword([]byte(acc.Auth.PasswordHash), pw) != nil {
			return nil, errAuth
		}
		return &ssh.Permissions{Extensions: map[string]string{
			extAccount: acc.Name,
			extMethod:  "publickey+password",
			extKeyFP:   keyFP,
		}}, nil
	}
	return nil, &ssh.PartialSuccessError{Next: ssh.ServerAuthCallbacks{PasswordCallback: secondStep}}
}

// password handles password-only logins, for accounts with mode "any".
// Accounts with mode "all" must start with their key, so a password alone
// is always refused for them.
func (a *authenticator) password(meta ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
	acc := a.account(meta.User())
	usable := acc != nil && acc.Auth.Mode == config.ModeAny && acc.Auth.PasswordHash != ""

	hash := dummyHash
	if usable {
		hash = []byte(acc.Auth.PasswordHash)
	}
	// Always run bcrypt, even for unknown users, so timing gives nothing away.
	err := bcrypt.CompareHashAndPassword(hash, pw)
	if !usable || err != nil {
		return nil, errAuth
	}

	return &ssh.Permissions{Extensions: map[string]string{
		extAccount: acc.Name,
		extMethod:  "password",
	}}, nil
}

// hasKey checks the key against all of the account's keys without stopping
// early, so timing doesn't reveal which key matched.
func hasKey(acc *config.Account, key []byte) bool {
	found := 0
	for _, k := range acc.Keys {
		found |= subtle.ConstantTimeCompare(k, key)
	}
	return found == 1
}

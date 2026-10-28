package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Options change how a file is loaded.
type Options struct {
	// Offline skips anything that needs secrets, files or the network,
	// so CI can check a pull request without production secrets.
	Offline bool
}

// refRe matches references like ${env:NAME}, ${file:/path} and ${secret:url}.
var refRe = regexp.MustCompile(`\$\{(\w+):([^}]*)\}`)

// Load reads a config file from disk and checks it.
func Load(path string, opt Options) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw, opt)
}

// Parse checks the YAML text and builds a Config from it.
// It returns every problem it finds, not just the first one.
func Parse(raw []byte, opt Options) (*Config, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	if err := movedQueue(&root); err != nil {
		return nil, err
	}

	// First pass on the original text, so unknown keys and wrong types are
	// reported with the real line numbers.
	strict := yaml.NewDecoder(bytes.NewReader(raw))
	strict.KnownFields(true)
	if err := strict.Decode(&Config{}); err != nil {
		return nil, err
	}

	refs, err := resolveRefs(&root, opt.Offline)
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	if err := root.Decode(cfg); err != nil {
		return nil, err
	}
	applyDefaults(cfg)
	if err := validate(cfg, refs, opt); err != nil {
		return nil, err
	}

	cfg.Hash = HashOf(raw)
	cfg.Raw = raw
	return cfg, nil
}

// movedQueue explains the one setting that moved: before named actions,
// "actions:" held the queue settings. Without this, an old file would fail
// with a confusing type error.
func movedQueue(root *yaml.Node) error {
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil
	}
	top := root.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value != "actions" {
			continue
		}
		actions := top.Content[i+1]
		for j := 0; j+1 < len(actions.Content); j += 2 {
			k := actions.Content[j]
			if (k.Value == "workers" || k.Value == "max_attempts" || k.Value == "backoff") && actions.Content[j+1].Kind == yaml.ScalarNode {
				return fmt.Errorf("line %d: actions.%s moved to server.queue.%s (actions: now holds named actions)", k.Line, k.Value, k.Value)
			}
		}
	}
	return nil
}

// HashOf returns the hash used to tell config versions apart.
func HashOf(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// resolveRefs replaces every ${env:...} and ${file:...} with its real value.
// It works on already-parsed values, so a secret can never change the
// YAML structure. It returns the YAML paths that held references.
func resolveRefs(root *yaml.Node, offline bool) (map[string]bool, error) {
	refs := map[string]bool{}
	var errs []error

	// A value reused with a YAML alias (*name) is one node seen at several
	// paths. Every path is marked first, then each node is resolved once.
	type found struct {
		path string
		node *yaml.Node
	}
	var all []found
	walk(root, "", func(path string, n *yaml.Node) {
		if refRe.MatchString(n.Value) {
			refs[path] = true
			all = append(all, found{path, n})
		}
	})

	done := map[*yaml.Node]bool{}
	for _, f := range all {
		path, n := f.path, f.node
		if done[n] {
			continue
		}
		done[n] = true
		n.Value = refRe.ReplaceAllStringFunc(n.Value, func(m string) string {
			parts := refRe.FindStringSubmatch(m)
			v, err := resolveRef(parts[1], parts[2], offline)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", path, err))
			}
			return v
		})
	}

	return refs, joinErrs(errs)
}

// resolveRef returns the value behind one reference.
// Offline, every reference becomes an empty string.
func resolveRef(kind, arg string, offline bool) (string, error) {
	if kind != "env" && kind != "file" && kind != "secret" {
		return "", fmt.Errorf("unsupported reference ${%s:...} (use env, file or secret)", kind)
	}
	if offline {
		return "", nil
	}

	switch kind {
	case "env":
		v, ok := os.LookupEnv(arg)
		if !ok {
			return "", fmt.Errorf("env %s not set", arg)
		}
		return v, nil
	case "secret":
		return readSecret(arg)
	}

	b, err := os.ReadFile(arg)
	if err != nil {
		return "", err
	}
	// Secret files usually end with a newline that isn't part of the secret.
	return strings.TrimRight(string(b), "\r\n"), nil
}

// walk calls fn for every plain value in the YAML tree, together with its
// path, for example "accounts[0].auth.password_hash".
func walk(n *yaml.Node, path string, fn func(string, *yaml.Node)) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			walk(c, path, fn)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			p := n.Content[i].Value
			if path != "" {
				p = path + "." + p
			}
			walk(n.Content[i+1], p, fn)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			walk(c, path+"["+strconv.Itoa(i)+"]", fn)
		}
	case yaml.AliasNode:
		walk(n.Alias, path, fn)
	case yaml.ScalarNode:
		fn(path, n)
	}
}

// applyDefaults fills in every setting the file left empty.
func applyDefaults(c *Config) {
	s := &c.Server
	setDefault(&s.Listen, ":2222")
	setDefault(&s.SpoolDir, "/var/lib/sftpgate/spool")
	setDefault(&s.IdleTimeout, 15*time.Minute)
	setDefault(&s.MaxAuthTries, 6)
	setDefault(&s.MaxConnections, 25)
	setDefault(&s.MinSpoolFree, 1<<30)
	setDefault(&s.ReloadInterval, 10*time.Second)
	setDefault(&s.Throttle.MaxFailures, 5)
	setDefault(&s.Throttle.Window, 10*time.Minute)
	setDefault(&s.Throttle.Block, 15*time.Minute)
	setDefault(&s.Timezone, "UTC")
	setDefault(&c.HTTP.Listen, ":8080")
	setDefault(&s.Queue.Workers, 4)
	setDefault(&s.Queue.MaxAttempts, 8)
	setDefault(&s.Queue.Backoff, 30*time.Second)

	for _, a := range c.Accounts {
		setDefault(&a.Auth.Mode, ModeAny)
	}
}

// setDefault sets *v to d when *v is still its zero value.
func setDefault[T comparable](v *T, d T) {
	var zero T
	if *v == zero {
		*v = d
	}
}

// fileRefRe matches ${file:/path} references.
var fileRefRe = regexp.MustCompile(`\$\{file:([^}]*)\}`)

// SourceHash returns a hash of the config file together with every file it
// references with ${file:...}. It changes when either one changes, so a
// rotated Kubernetes Secret is picked up without a restart.
func SourceHash(raw []byte) string {
	h := sha256.New()
	h.Write(raw)
	for _, m := range fileRefRe.FindAllSubmatch(raw, -1) {
		h.Write([]byte{0})
		h.Write(m[1])
		// A missing file still counts: it shows up as a change once it appears.
		if b, err := os.ReadFile(string(m[1])); err == nil {
			h.Write(b)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

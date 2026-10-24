package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Revision returns the git commit the config file comes from, or "".
//
// SFTPGATE_REVISION wins when set, for example by a CI deploy. Otherwise,
// when git-sync delivers the config, the file's real path runs through a
// folder named after the commit (like /git/.worktrees/<sha>/...), so the
// commit is read from there.
func Revision(configPath string) string {
	if rev := os.Getenv("SFTPGATE_REVISION"); rev != "" {
		return rev
	}
	resolved, err := filepath.EvalSymlinks(configPath)
	if err != nil {
		return ""
	}
	for _, part := range strings.Split(filepath.ToSlash(resolved), "/") {
		if commitRe.MatchString(part) {
			return part
		}
	}
	return ""
}

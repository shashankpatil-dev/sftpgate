// Command sftpgate runs the SFTP gateway and its helper commands.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	// The container image has no time zone files, so embed them for server.timezone.
	_ "time/tzdata"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"github.com/shashankpatil-dev/sftpgate/internal/config"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `sftpgate - a secure SFTP gateway configured from one YAML file

Usage:
  sftpgate serve [-config FILE]         run the server
  sftpgate validate [-offline] FILE     check a config file
  sftpgate hash-password [-cost N]      make a bcrypt hash for password_hash
  sftpgate version                      print the version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "serve":
		err = serveCmd(args)
	case "validate":
		err = validateCmd(args)
	case "hash-password":
		err = hashPasswordCmd(args)
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func serveCmd(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	defaultPath := os.Getenv("SFTPGATE_CONFIG")
	if defaultPath == "" {
		defaultPath = "/etc/sftpgate/sftpgate.yaml"
	}
	path := fs.String("config", defaultPath, "config file (env SFTPGATE_CONFIG)")
	_ = fs.Parse(args)
	return serve(*path)
}

// validateCmd checks a config file and prints every problem it finds.
// CI runs it with -offline on every pull request.
func validateCmd(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	offline := fs.Bool("offline", false, "skip checks that need secrets, files or the network")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: sftpgate validate [-offline] FILE")
	}

	if _, err := config.Load(fs.Arg(0), config.Options{Offline: *offline}); err != nil {
		return fmt.Errorf("%s is not valid:\n%w", fs.Arg(0), err)
	}
	fmt.Println(fs.Arg(0), "is valid")
	return nil
}

// hashPasswordCmd asks for a password and prints its bcrypt hash.
// In a terminal it asks twice without showing what is typed. Piped input
// is read from the first line, for scripts.
func hashPasswordCmd(args []string) error {
	fs := flag.NewFlagSet("hash-password", flag.ExitOnError)
	cost := fs.Int("cost", 12, "bcrypt cost, 10 or more")
	_ = fs.Parse(args)
	if *cost < 10 {
		return errors.New("cost must be 10 or more")
	}

	pw, err := readPassword()
	if err != nil {
		return err
	}
	if pw == "" {
		return errors.New("empty password")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(pw), *cost)
	if err != nil {
		return err
	}
	fmt.Println(string(hash))
	return nil
}

func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}

	fmt.Fprint(os.Stderr, "Password: ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Again: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("passwords do not match")
	}
	return string(first), nil
}

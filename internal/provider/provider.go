// Package provider adapts each AI CLI's on-disk/Keychain login state to a
// common interface so accounts can be captured, stored and re-applied.
package provider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Danendz/multi_ai/internal/secret"
)

var ErrNotLoggedIn = errors.New("not logged in")

// Creds is an opaque, provider-specific JSON blob holding one account's login.
type Creds []byte

// A home of "" means the tool's default (global) location; otherwise it is an
// isolated config directory used via the tool's env override.
type Provider interface {
	Name() string
	Binary() string
	Capture(home string) (Creds, error)
	Apply(home string, c Creds) error
	// Identity is a human-readable account id, usually the email.
	Identity(c Creds) string
	// Freshness orders copies of the same account: refresh tokens rotate, so
	// the copy refreshed most recently is the only one guaranteed to work.
	Freshness(c Creds) time.Time
	LoginCmd(home string) *exec.Cmd
	Env(home string) []string
	// DefaultDir is where shared config lives; Shared lists entries in it to
	// symlink into isolated homes.
	DefaultDir() string
	Shared() []string
	// ConfigDir is where Shared entries are linked for a given isolated home.
	ConfigDir(home string) string
}

type Deps struct {
	HomeDir  string
	Keychain secret.Store
	User     string
}

func DefaultDeps() (Deps, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return Deps{}, err
	}
	return Deps{HomeDir: h, Keychain: secret.Keychain{}, User: os.Getenv("USER")}, nil
}

func All(d Deps) []Provider {
	return []Provider{&Claude{d}, &Codex{d}, &Gemini{d}}
}

func Get(d Deps, name string) (Provider, error) {
	for _, p := range All(d) {
		if p.Name() == name {
			return p, nil
		}
	}
	return nil, fmt.Errorf("unknown provider %q (have: %s)", name, strings.Join(Names(), ", "))
}

func Names() []string {
	n := []string{}
	for _, p := range All(Deps{}) {
		n = append(n, p.Name())
	}
	sort.Strings(n)
	return n
}

// LinkShared symlinks shared config entries from the default dir into the
// isolated config dir, leaving anything already present alone.
func LinkShared(p Provider, home string) error {
	dst := p.ConfigDir(home)
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	for _, name := range p.Shared() {
		src := filepath.Join(p.DefaultDir(), name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		target := filepath.Join(dst, name)
		if _, err := os.Lstat(target); err == nil {
			continue
		}
		if err := os.Symlink(src, target); err != nil {
			return err
		}
	}
	return nil
}

// writeFileAtomic replaces path without leaving a half-written file behind.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// jwtClaims decodes a JWT payload without verifying it; only used for display.
func jwtClaims(tok string) map[string]any {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func msTime(v float64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(v))
}

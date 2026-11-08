// Package secret stores opaque blobs in the macOS Keychain.
//
// It shells out to /usr/bin/security instead of linking Security.framework:
// items created by the `security` tool are readable by other tools that also
// use it (Claude Code does), so writing into Claude's own items never triggers
// a Keychain access dialog.
package secret

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

var ErrNotFound = errors.New("secret not found")

type Store interface {
	Get(service, account string) ([]byte, error)
	Set(service, account string, data []byte) error
	Delete(service, account string) error
}

type Keychain struct{}

func (Keychain) Get(service, account string) ([]byte, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", service, "-a", account, "-w").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 44 {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("keychain read %q: %w", service, err)
	}
	out = bytes.TrimRight(out, "\n")
	// `security -w` prints non-printable data as hex.
	if len(out) > 0 && out[0] != '{' {
		if dec, err := hex.DecodeString(string(out)); err == nil {
			return dec, nil
		}
	}
	return out, nil
}

// Set creates or updates the item. The value is passed hex-encoded on argv:
// `security -i` truncates lines at ~4KB, which Claude's item exceeds.
func (Keychain) Set(service, account string, data []byte) error {
	cmd := exec.Command("security", "add-generic-password", "-U", "-s", service, "-a", account, "-X", hex.EncodeToString(data))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain write %q: %v: %s", service, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (Keychain) Delete(service, account string) error {
	err := exec.Command("security", "delete-generic-password", "-s", service, "-a", account).Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 44 {
		return ErrNotFound
	}
	return err
}

// Memory is an in-process Store for tests.
type Memory struct {
	mu sync.Mutex
	m  map[string][]byte
}

func NewMemory() *Memory { return &Memory{m: map[string][]byte{}} }

func (s *Memory) Get(service, account string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[service+"\x00"+account]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

func (s *Memory) Set(service, account string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[service+"\x00"+account] = append([]byte(nil), data...)
	return nil
}

func (s *Memory) Delete(service, account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := service + "\x00" + account
	if _, ok := s.m[k]; !ok {
		return ErrNotFound
	}
	delete(s.m, k)
	return nil
}

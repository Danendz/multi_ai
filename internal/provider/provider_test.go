package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Danendz/multi_ai/internal/secret"
)

func TestClaudeKeychainIDMatchesClaudeCode(t *testing.T) {
	c := &Claude{Deps{User: "dev"}}
	svc, acct := c.keychainID("")
	if svc != "Claude Code-credentials" || acct != "dev" {
		t.Fatalf("got %q %q", svc, acct)
	}
	// Expected suffix from `printf /tmp/x | shasum -a 256 | cut -c1-8`.
	svc, _ = c.keychainID("/tmp/x")
	if svc != "Claude Code-credentials-2e56aa36" {
		t.Fatalf("got %q", svc)
	}
	c = &Claude{Deps{User: "has space"}}
	if _, acct := c.keychainID(""); acct != "claude-code-user" {
		t.Fatalf("got %q", acct)
	}
}

func TestGeminiRoundTrip(t *testing.T) {
	home := t.TempDir()
	g := &Gemini{Deps{HomeDir: home, Keychain: secret.NewMemory()}}
	if _, err := g.Capture(""); err != ErrNotLoggedIn {
		t.Fatalf("want ErrNotLoggedIn, got %v", err)
	}
	dir := filepath.Join(home, ".gemini")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "oauth_creds.json"), []byte(`{"refresh_token":"r","expiry_date":42}`), 0o600)
	os.WriteFile(filepath.Join(dir, "google_accounts.json"), []byte(`{"active":"g@x.dev","old":[]}`), 0o600)
	c, err := g.Capture("")
	if err != nil {
		t.Fatal(err)
	}
	if g.Identity(c) != "g@x.dev" || g.Freshness(c).UnixMilli() != 42 {
		t.Fatalf("identity/freshness wrong: %s", c)
	}
	iso := filepath.Join(home, "iso")
	if err := g.Apply(iso, c); err != nil {
		t.Fatal(err)
	}
	c2, err := g.Capture(iso)
	if err != nil || g.Identity(c2) != "g@x.dev" {
		t.Fatalf("isolated capture: %v", err)
	}
}

func TestCodexKeyringStoreIsReported(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte("cli_auth_credentials_store = \"keyring\"\n"), 0o600)
	c := &Codex{Deps{HomeDir: home}}
	if _, err := c.Capture(""); err == nil || err == ErrNotLoggedIn {
		t.Fatalf("want explicit keyring error, got %v", err)
	}
}

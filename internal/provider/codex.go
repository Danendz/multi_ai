package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

// Codex CLI stores its login in $CODEX_HOME/auth.json (default ~/.codex).
// The optional keyring store (cli_auth_credentials_store) is not supported.
type Codex struct{ d Deps }

func (c *Codex) Name() string       { return "codex" }
func (c *Codex) Binary() string     { return "codex" }
func (c *Codex) DefaultDir() string { return filepath.Join(c.d.HomeDir, ".codex") }
func (c *Codex) ConfigDir(home string) string {
	return home
}
func (c *Codex) Shared() []string {
	return []string{"config.toml", "AGENTS.md", "skills", "plugins", "rules", "prompts", "hooks.json"}
}

func (c *Codex) Env(home string) []string { return []string{"CODEX_HOME=" + home} }

func (c *Codex) LoginCmd(home string) *exec.Cmd {
	cmd := exec.Command("codex", "login", "-c", `cli_auth_credentials_store="file"`)
	cmd.Env = append(os.Environ(), c.Env(home)...)
	return cmd
}

func (c *Codex) dir(home string) string {
	if home == "" {
		return c.DefaultDir()
	}
	return home
}

var codexKeyringRe = regexp.MustCompile(`(?m)^\s*cli_auth_credentials_store\s*=\s*"(keyring|auto)"`)

func (c *Codex) Capture(home string) (Creds, error) {
	b, err := os.ReadFile(filepath.Join(c.dir(home), "auth.json"))
	if errors.Is(err, os.ErrNotExist) {
		if cfg, _ := os.ReadFile(filepath.Join(c.dir(home), "config.toml")); codexKeyringRe.Match(cfg) {
			return nil, fmt.Errorf("codex uses the keyring credential store, which is not supported; set cli_auth_credentials_store = \"file\"")
		}
		return nil, ErrNotLoggedIn
	}
	if err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("codex auth.json is not valid JSON")
	}
	return b, nil
}

func (c *Codex) Apply(home string, cr Creds) error {
	if !json.Valid(cr) {
		return fmt.Errorf("invalid codex creds")
	}
	return writeFileAtomic(filepath.Join(c.dir(home), "auth.json"), cr, 0o600)
}

type codexAuth struct {
	APIKey      *string `json:"OPENAI_API_KEY"`
	LastRefresh string  `json:"last_refresh"`
	Tokens      *struct {
		IDToken string `json:"id_token"`
	} `json:"tokens"`
}

func (c *Codex) Identity(cr Creds) string {
	var a codexAuth
	if json.Unmarshal(cr, &a) != nil {
		return "unknown"
	}
	if a.Tokens != nil {
		if email, _ := jwtClaims(a.Tokens.IDToken)["email"].(string); email != "" {
			return email
		}
	}
	if a.APIKey != nil && len(*a.APIKey) > 8 {
		k := *a.APIKey
		return "api-key …" + k[len(k)-4:]
	}
	return "unknown"
}

func (c *Codex) Freshness(cr Creds) time.Time {
	var a codexAuth
	if json.Unmarshal(cr, &a) != nil {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, a.LastRefresh)
	return t
}

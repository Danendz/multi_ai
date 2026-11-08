package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/Danendz/multi_ai/internal/secret"
)

// Claude Code keeps the OAuth token in the Keychain item "Claude Code-credentials"
// (suffixed with sha256(CLAUDE_CONFIG_DIR)[:8] when that env var is set) and the
// account profile under "oauthAccount" in .claude.json. The Keychain item also
// carries MCP server tokens, which are left untouched on swap.
type Claude struct{ d Deps }

type claudeCreds struct {
	OAuth   json.RawMessage `json:"claudeAiOauth"`
	Account json.RawMessage `json:"oauthAccount,omitempty"`
}

func (c *Claude) Name() string       { return "claude" }
func (c *Claude) Binary() string     { return "claude" }
func (c *Claude) DefaultDir() string { return filepath.Join(c.d.HomeDir, ".claude") }
func (c *Claude) ConfigDir(home string) string {
	return home
}
func (c *Claude) Shared() []string {
	return []string{"settings.json", "CLAUDE.md", "skills", "plugins", "agents", "commands", "hooks", "output-styles", "keybindings.json", "statusline.sh"}
}

func (c *Claude) Env(home string) []string { return []string{"CLAUDE_CONFIG_DIR=" + home} }

func (c *Claude) LoginCmd(home string) *exec.Cmd {
	cmd := exec.Command("claude", "auth", "login")
	cmd.Env = append(os.Environ(), c.Env(home)...)
	return cmd
}

var claudeUserRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// Mirrors Claude Code's own service/account naming.
func (c *Claude) keychainID(home string) (service, account string) {
	service = "Claude Code-credentials"
	if home != "" {
		sum := sha256.Sum256([]byte(home))
		service += "-" + hex.EncodeToString(sum[:])[:8]
	}
	account = c.d.User
	if !claudeUserRe.MatchString(account) {
		account = "claude-code-user"
	}
	return service, account
}

func (c *Claude) stateFile(home string) string {
	if home == "" {
		return filepath.Join(c.d.HomeDir, ".claude.json")
	}
	return filepath.Join(home, ".claude.json")
}

func (c *Claude) Capture(home string) (Creds, error) {
	svc, acct := c.keychainID(home)
	raw, err := c.d.Keychain.Get(svc, acct)
	if errors.Is(err, secret.ErrNotFound) {
		return nil, ErrNotLoggedIn
	}
	if err != nil {
		return nil, err
	}
	var item map[string]json.RawMessage
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, fmt.Errorf("parse claude keychain item: %w", err)
	}
	oauth := item["claudeAiOauth"]
	if len(oauth) == 0 || string(oauth) == "null" {
		return nil, ErrNotLoggedIn
	}
	state, err := readJSONMap(c.stateFile(home))
	if err != nil {
		return nil, err
	}
	return json.Marshal(claudeCreds{OAuth: oauth, Account: state["oauthAccount"]})
}

func (c *Claude) Apply(home string, cr Creds) error {
	var cc claudeCreds
	if err := json.Unmarshal(cr, &cc); err != nil || len(cc.OAuth) == 0 {
		return fmt.Errorf("invalid claude creds")
	}
	svc, acct := c.keychainID(home)
	item := map[string]json.RawMessage{}
	if raw, err := c.d.Keychain.Get(svc, acct); err == nil {
		if err := json.Unmarshal(raw, &item); err != nil {
			return fmt.Errorf("parse claude keychain item: %w", err)
		}
	} else if !errors.Is(err, secret.ErrNotFound) {
		return err
	}
	item["claudeAiOauth"] = cc.OAuth
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if err := c.d.Keychain.Set(svc, acct, raw); err != nil {
		return err
	}

	path := c.stateFile(home)
	state, err := readJSONMap(path)
	if err != nil {
		return err
	}
	if len(state) == 0 {
		// Fresh isolated home: skip Claude's first-run onboarding.
		state["hasCompletedOnboarding"] = json.RawMessage("true")
	}
	if len(cc.Account) > 0 {
		state["oauthAccount"] = cc.Account
	} else {
		delete(state, "oauthAccount")
	}
	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, out, filePerm(path, 0o600))
}

func (c *Claude) Identity(cr Creds) string {
	var cc claudeCreds
	var acct struct {
		Email string `json:"emailAddress"`
		Org   string `json:"organizationName"`
	}
	if json.Unmarshal(cr, &cc) != nil || json.Unmarshal(cc.Account, &acct) != nil || acct.Email == "" {
		return "unknown"
	}
	return acct.Email
}

func (c *Claude) Freshness(cr Creds) time.Time {
	var cc claudeCreds
	var o struct {
		ExpiresAt float64 `json:"expiresAt"`
	}
	if json.Unmarshal(cr, &cc) != nil || json.Unmarshal(cc.OAuth, &o) != nil {
		return time.Time{}
	}
	return msTime(o.ExpiresAt)
}

// Forget deletes the Keychain item Claude created for an isolated home.
func (c *Claude) Forget(home string) error {
	svc, acct := c.keychainID(home)
	return c.d.Keychain.Delete(svc, acct)
}

func readJSONMap(path string) (map[string]json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

func filePerm(path string, def os.FileMode) os.FileMode {
	if fi, err := os.Stat(path); err == nil {
		return fi.Mode().Perm()
	}
	return def
}

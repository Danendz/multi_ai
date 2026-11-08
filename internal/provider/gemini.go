package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Gemini CLI keeps Google OAuth tokens in ~/.gemini/oauth_creds.json and the
// active account in ~/.gemini/google_accounts.json. Isolated homes rely on
// GEMINI_CLI_HOME, which relocates the whole ~/.gemini directory.
type Gemini struct{ d Deps }

type geminiCreds struct {
	OAuth    json.RawMessage `json:"oauth_creds"`
	Accounts json.RawMessage `json:"google_accounts,omitempty"`
}

func (g *Gemini) Name() string       { return "gemini" }
func (g *Gemini) Binary() string     { return "gemini" }
func (g *Gemini) DefaultDir() string { return filepath.Join(g.d.HomeDir, ".gemini") }
func (g *Gemini) ConfigDir(home string) string {
	return filepath.Join(home, ".gemini")
}
func (g *Gemini) Shared() []string {
	return []string{"settings.json", "GEMINI.md", "extensions", "commands"}
}

func (g *Gemini) Env(home string) []string { return []string{"GEMINI_CLI_HOME=" + home} }

// Gemini has no login subcommand: the user signs in from the interactive UI and quits.
func (g *Gemini) LoginCmd(home string) *exec.Cmd {
	cmd := exec.Command("gemini")
	cmd.Env = append(os.Environ(), g.Env(home)...)
	return cmd
}

func (g *Gemini) dir(home string) string {
	if home == "" {
		return g.DefaultDir()
	}
	return g.ConfigDir(home)
}

func (g *Gemini) Capture(home string) (Creds, error) {
	oauth, err := os.ReadFile(filepath.Join(g.dir(home), "oauth_creds.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotLoggedIn
	}
	if err != nil {
		return nil, err
	}
	accts, err := os.ReadFile(filepath.Join(g.dir(home), "google_accounts.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if !json.Valid(oauth) || (accts != nil && !json.Valid(accts)) {
		return nil, fmt.Errorf("gemini credential files are not valid JSON")
	}
	return json.Marshal(geminiCreds{OAuth: oauth, Accounts: accts})
}

func (g *Gemini) Apply(home string, cr Creds) error {
	var gc geminiCreds
	if err := json.Unmarshal(cr, &gc); err != nil || len(gc.OAuth) == 0 {
		return fmt.Errorf("invalid gemini creds")
	}
	if err := writeFileAtomic(filepath.Join(g.dir(home), "oauth_creds.json"), gc.OAuth, 0o600); err != nil {
		return err
	}
	if len(gc.Accounts) > 0 {
		return writeFileAtomic(filepath.Join(g.dir(home), "google_accounts.json"), gc.Accounts, 0o600)
	}
	return nil
}

func (g *Gemini) Identity(cr Creds) string {
	var gc geminiCreds
	var a struct {
		Active string `json:"active"`
	}
	if json.Unmarshal(cr, &gc) == nil && json.Unmarshal(gc.Accounts, &a) == nil && a.Active != "" {
		return a.Active
	}
	var o struct {
		IDToken string `json:"id_token"`
	}
	if json.Unmarshal(gc.OAuth, &o) == nil {
		if email, _ := jwtClaims(o.IDToken)["email"].(string); email != "" {
			return email
		}
	}
	return "unknown"
}

func (g *Gemini) Freshness(cr Creds) time.Time {
	var gc geminiCreds
	var o struct {
		Expiry float64 `json:"expiry_date"`
	}
	if json.Unmarshal(cr, &gc) != nil || json.Unmarshal(gc.OAuth, &o) != nil {
		return time.Time{}
	}
	return msTime(o.Expiry)
}

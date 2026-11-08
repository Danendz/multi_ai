package swap

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Danendz/multi_ai/internal/provider"
	"github.com/Danendz/multi_ai/internal/secret"
)

type env struct {
	t     *testing.T
	home  string
	store *secret.Memory
	deps  provider.Deps
	m     *Manager
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	store := secret.NewMemory()
	deps := provider.Deps{HomeDir: home, Keychain: store, User: "tester"}
	m, err := New(filepath.Join(home, ".ai-switch"), store, deps, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	m.Running = func(string) bool { return false }
	return &env{t: t, home: home, store: store, deps: deps, m: m}
}

func (e *env) p(name string) provider.Provider {
	p, err := e.m.Provider(name)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

// loginClaude simulates Claude Code's own global login state.
func (e *env) loginClaude(email string, expires int64) {
	e.t.Helper()
	item := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"at-%s-%d","refreshToken":"rt-%s-%d","expiresAt":%d},"mcpOAuth":{"srv":"keep"}}`, email, expires, email, expires, expires)
	if err := e.store.Set("Claude Code-credentials", "tester", []byte(item)); err != nil {
		e.t.Fatal(err)
	}
	state := map[string]any{"numStartups": 7, "oauthAccount": map[string]any{"emailAddress": email}}
	b, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(e.home, ".claude.json"), b, 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) loginCodex(email string, refreshed time.Time) {
	e.t.Helper()
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"email":%q}`, email)))
	auth := fmt.Sprintf(`{"OPENAI_API_KEY":null,"tokens":{"id_token":"h.%s.s","refresh_token":"rt-%s"},"last_refresh":%q}`, payload, email, refreshed.Format(time.RFC3339Nano))
	dir := filepath.Join(e.home, ".codex")
	os.MkdirAll(dir, 0o700)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(auth), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) live(name string) (string, provider.Creds) {
	e.t.Helper()
	p := e.p(name)
	c, err := p.Capture("")
	if err != nil {
		e.t.Fatalf("capture %s: %v", name, err)
	}
	return p.Identity(c), c
}

func TestImportThenSwitchRoundTrip(t *testing.T) {
	e := newEnv(t)
	e.loginClaude("me@home.dev", 1000)
	e.loginCodex("me@home.dev", time.Unix(100, 0))
	if err := e.m.Import("personal", nil); err != nil {
		t.Fatal(err)
	}

	// Simulate `ai add claude work` having stored a second account.
	e.loginClaude("me@work.dev", 2000)
	work, _ := e.p("claude").Capture("")
	if err := e.m.saveSlot("work", e.p("claude"), work); err != nil {
		t.Fatal(err)
	}
	e.loginClaude("me@home.dev", 1000)

	if err := e.m.SwitchProfile("work"); err != nil {
		t.Fatal(err)
	}
	if id, _ := e.live("claude"); id != "me@work.dev" {
		t.Fatalf("claude live = %s", id)
	}
	if id, _ := e.live("codex"); id != "me@home.dev" {
		t.Fatalf("codex should be untouched (work has no codex), got %s", id)
	}
	if e.m.Reg.Active["claude"] != "work" || e.m.Reg.Active["codex"] != "personal" {
		t.Fatalf("active = %v", e.m.Reg.Active)
	}

	if err := e.m.SwitchProfile("personal"); err != nil {
		t.Fatal(err)
	}
	if id, _ := e.live("claude"); id != "me@home.dev" {
		t.Fatalf("claude live = %s", id)
	}
}

func TestSwitchSavesBackRefreshedToken(t *testing.T) {
	e := newEnv(t)
	e.loginClaude("a@x.dev", 1000)
	e.m.Import("a", nil)
	e.loginClaude("b@x.dev", 1000)
	e.m.Import("b", nil)

	// b is live; Claude refreshes its token while in use.
	e.loginClaude("b@x.dev", 9000)
	if err := e.m.Switch(e.p("claude"), "a"); err != nil {
		t.Fatal(err)
	}
	slot, _ := e.m.slot("b", e.p("claude"))
	if got := e.p("claude").Freshness(slot).UnixMilli(); got != 9000 {
		t.Fatalf("b slot not saved back: expiresAt=%d", got)
	}
}

func TestClaudeApplyPreservesOtherState(t *testing.T) {
	e := newEnv(t)
	e.loginClaude("a@x.dev", 1000)
	e.m.Import("a", nil)
	e.loginClaude("b@x.dev", 1000)
	e.m.Import("b", nil)
	if err := e.m.Switch(e.p("claude"), "a"); err != nil {
		t.Fatal(err)
	}
	raw, _ := e.store.Get("Claude Code-credentials", "tester")
	var item map[string]json.RawMessage
	json.Unmarshal(raw, &item)
	if string(item["mcpOAuth"]) != `{"srv":"keep"}` {
		t.Fatalf("mcpOAuth lost: %s", raw)
	}
	b, _ := os.ReadFile(filepath.Join(e.home, ".claude.json"))
	var state map[string]any
	json.Unmarshal(b, &state)
	if state["numStartups"] != float64(7) {
		t.Fatalf("other .claude.json keys lost: %s", b)
	}
}

func TestSwitchAutoSavesUnknownLiveAccount(t *testing.T) {
	e := newEnv(t)
	e.loginClaude("a@x.dev", 1000)
	e.m.Import("a", nil)
	// User logged in manually to an account ai-switch has never seen.
	e.loginClaude("stray@x.dev", 1000)
	if err := e.m.Switch(e.p("claude"), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.slot("stray@x.dev", e.p("claude")); err != nil {
		t.Fatalf("stray account not preserved: %v", err)
	}
}

func TestSwitchUnknownProfile(t *testing.T) {
	e := newEnv(t)
	e.loginClaude("a@x.dev", 1000)
	e.m.Import("a", nil)
	if err := e.m.Switch(e.p("claude"), "nope"); err == nil {
		t.Fatal("expected error")
	}
	if id, _ := e.live("claude"); id != "a@x.dev" {
		t.Fatal("live login changed on failed switch")
	}
}

func TestIsolatedSeedAndSyncBack(t *testing.T) {
	e := newEnv(t)
	os.MkdirAll(filepath.Join(e.home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(e.home, ".claude", "settings.json"), []byte(`{}`), 0o600)
	e.loginClaude("w@x.dev", 1000)
	e.m.Import("work", nil)
	e.loginClaude("p@x.dev", 1000)
	e.m.Import("personal", nil)

	p := e.p("claude")
	home, err := e.m.PrepareIsolated(p, "work")
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.Capture(home)
	if err != nil || p.Identity(c) != "w@x.dev" {
		t.Fatalf("isolated not seeded: %v %s", err, p.Identity(c))
	}
	if fi, err := os.Lstat(filepath.Join(home, "settings.json")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("settings.json not symlinked: %v", err)
	}
	if id, _ := e.live("claude"); id != "p@x.dev" {
		t.Fatal("isolated run must not touch the global login")
	}

	// The isolated session refreshes its token; it must flow back to the slot.
	c2 := []byte(`{"claudeAiOauth":{"accessToken":"new","expiresAt":5000},"oauthAccount":{"emailAddress":"w@x.dev"}}`)
	if err := p.Apply(home, c2); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Sync("work", p); err != nil {
		t.Fatal(err)
	}
	slot, _ := e.m.slot("work", p)
	if p.Freshness(slot).UnixMilli() != 5000 {
		t.Fatal("slot not updated from isolated home")
	}
}

func TestRemove(t *testing.T) {
	e := newEnv(t)
	e.loginCodex("a@x.dev", time.Unix(1, 0))
	e.m.Import("a", nil)
	if _, err := e.m.PrepareIsolated(e.p("codex"), "a"); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Remove("a", nil); err != nil {
		t.Fatal(err)
	}
	if len(e.m.Reg.Profiles) != 0 {
		t.Fatalf("profiles left: %v", e.m.Reg.Profiles)
	}
	if _, err := os.Stat(e.m.IsolatedHome("a", e.p("codex"))); !os.IsNotExist(err) {
		t.Fatal("isolated home not removed")
	}
}

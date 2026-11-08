// Package swap orchestrates moving accounts between three places:
//   - the profile slot (Keychain item "ai-switch" / "<profile>/<provider>"),
//   - the tool's global login (what a plain `claude` uses),
//   - an isolated home (~/.ai-switch/profiles/<profile>/<provider>).
//
// Refresh tokens rotate, so whenever the same account exists in more than one
// place the freshest copy is propagated to the others (see Sync).
package swap

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Danendz/multi_ai/internal/profile"
	"github.com/Danendz/multi_ai/internal/provider"
	"github.com/Danendz/multi_ai/internal/secret"
)

const slotService = "ai-switch"

type Manager struct {
	Root      string
	Store     secret.Store
	Reg       *profile.Registry
	Providers []provider.Provider
	Out       io.Writer
	// Running reports whether a process with the given binary name is running.
	Running func(binary string) bool
}

func New(root string, store secret.Store, deps provider.Deps, out io.Writer) (*Manager, error) {
	reg, err := profile.Load(filepath.Join(root, "config.json"))
	if err != nil {
		return nil, err
	}
	return &Manager{Root: root, Store: store, Reg: reg, Providers: provider.All(deps), Out: out, Running: pgrep}, nil
}

func pgrep(binary string) bool {
	return exec.Command("pgrep", "-x", binary).Run() == nil
}

func (m *Manager) Provider(name string) (provider.Provider, error) {
	for _, p := range m.Providers {
		if p.Name() == name {
			return p, nil
		}
	}
	return nil, fmt.Errorf("unknown provider %q", name)
}

func (m *Manager) IsProvider(name string) bool {
	_, err := m.Provider(name)
	return err == nil
}

func (m *Manager) IsolatedHome(prof string, p provider.Provider) string {
	return filepath.Join(m.Root, "profiles", prof, p.Name())
}

func (m *Manager) slot(prof string, p provider.Provider) (provider.Creds, error) {
	return m.Store.Get(slotService, prof+"/"+p.Name())
}

func (m *Manager) saveSlot(prof string, p provider.Provider, c provider.Creds) error {
	if err := m.Store.Set(slotService, prof+"/"+p.Name(), c); err != nil {
		return err
	}
	m.Reg.Set(prof, p.Name(), p.Identity(c))
	return nil
}

func (m *Manager) logf(format string, a ...any) { fmt.Fprintf(m.Out, format+"\n", a...) }

func (m *Manager) isoCaptured(prof string, p provider.Provider) (provider.Creds, error) {
	home := m.IsolatedHome(prof, p)
	if _, err := os.Stat(home); err != nil {
		return nil, provider.ErrNotLoggedIn
	}
	return p.Capture(home)
}

// Sync makes the slot, the isolated home and (if it holds the same account)
// the global login all carry the freshest copy of the profile's account.
func (m *Manager) Sync(prof string, p provider.Provider) error {
	id, ok := m.Reg.Identity(prof, p.Name())
	if !ok {
		return nil
	}
	type loc struct {
		home  string // "" global
		creds provider.Creds
		write func(provider.Creds) error
	}
	var locs []loc
	slot, err := m.slot(prof, p)
	if err != nil && !errors.Is(err, secret.ErrNotFound) {
		return err
	}
	if slot != nil {
		locs = append(locs, loc{creds: slot, write: func(c provider.Creds) error { return m.saveSlot(prof, p, c) }})
	}
	iso := m.IsolatedHome(prof, p)
	if c, err := m.isoCaptured(prof, p); err == nil && p.Identity(c) == id {
		locs = append(locs, loc{home: iso, creds: c, write: func(c provider.Creds) error { return p.Apply(iso, c) }})
	}
	if c, err := p.Capture(""); err == nil && p.Identity(c) == id {
		locs = append(locs, loc{creds: c, write: func(c provider.Creds) error { return p.Apply("", c) }})
	}
	if len(locs) == 0 {
		return nil
	}
	best := locs[0]
	for _, l := range locs[1:] {
		if p.Freshness(l.creds).After(p.Freshness(best.creds)) {
			best = l
		}
	}
	for _, l := range locs {
		if !bytes.Equal(l.creds, best.creds) {
			if err := l.write(best.creds); err != nil {
				return err
			}
		}
	}
	return nil
}

// Switch makes profile `to` the global login for provider p.
func (m *Manager) Switch(p provider.Provider, to string) error {
	target, err := m.slot(to, p)
	if errors.Is(err, secret.ErrNotFound) {
		return fmt.Errorf("profile %q has no %s account (add one: ai add %s %s)", to, p.Name(), p.Name(), to)
	}
	if err != nil {
		return err
	}

	live, liveErr := p.Capture("")
	if liveErr != nil && !errors.Is(liveErr, provider.ErrNotLoggedIn) {
		return liveErr
	}
	if live != nil {
		liveID := p.Identity(live)
		if liveID == p.Identity(target) {
			// Already logged in as the target account; just reconcile copies.
			if err := m.Sync(to, p); err != nil {
				return err
			}
			m.Reg.Active[p.Name()] = to
			m.logf("%s: already on %s (%s)", p.Name(), to, liveID)
			return m.Reg.Save()
		}
		if err := m.Store.Set(slotService, "_backup/"+p.Name(), live); err != nil {
			return fmt.Errorf("backup current %s login: %w", p.Name(), err)
		}
		// Save-back: never lose the outgoing account's freshest token.
		owner, ok := m.Reg.Owner(p.Name(), liveID)
		if ok {
			if err := m.Sync(owner, p); err != nil {
				return err
			}
		} else {
			owner = liveID
			if err := m.saveSlot(owner, p, live); err != nil {
				return err
			}
			m.logf("%s: current account %s was not in any profile; saved it as profile %q", p.Name(), liveID, owner)
		}
	}

	if err := m.Sync(to, p); err != nil {
		return err
	}
	if target, err = m.slot(to, p); err != nil {
		return err
	}
	if err := p.Apply("", target); err != nil {
		if live != nil {
			if rerr := p.Apply("", live); rerr != nil {
				return fmt.Errorf("apply failed: %v; rollback also failed: %v (backup in Keychain: %s/_backup/%s)", err, rerr, slotService, p.Name())
			}
		}
		return fmt.Errorf("apply %s: %w (rolled back)", to, err)
	}
	m.Reg.Active[p.Name()] = to
	if err := m.Reg.Save(); err != nil {
		return err
	}
	m.logf("%s → %s (%s)", p.Name(), to, p.Identity(target))
	if m.Running(p.Binary()) {
		m.logf("  ⚠ %s is running: open sessions keep the old account and may write it back.\n    Restart them, or use `ai run %s %s` for side-by-side accounts.", p.Binary(), p.Name(), to)
	}
	return nil
}

// SwitchProfile switches every provider that has a slot in prof.
func (m *Manager) SwitchProfile(prof string) error {
	slots, ok := m.Reg.Profiles[prof]
	if !ok {
		return fmt.Errorf("no profile %q (have: %v)", prof, m.Reg.Names())
	}
	var errs []error
	for _, p := range m.Providers {
		if _, ok := slots[p.Name()]; ok {
			if err := m.Switch(p, prof); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
			}
		}
	}
	return errors.Join(errs...)
}

// Import saves the currently logged-in global accounts into prof.
func (m *Manager) Import(prof string, only []provider.Provider) error {
	ps := only
	if len(ps) == 0 {
		ps = m.Providers
	}
	found := false
	for _, p := range ps {
		c, err := p.Capture("")
		if errors.Is(err, provider.ErrNotLoggedIn) {
			continue
		}
		if err != nil {
			m.logf("%s: skipped: %v", p.Name(), err)
			continue
		}
		found = true
		id := p.Identity(c)
		if owner, ok := m.Reg.Owner(p.Name(), id); ok && owner != prof {
			m.logf("%s: %s is already saved in profile %q", p.Name(), id, owner)
			m.Reg.Active[p.Name()] = owner
			continue
		}
		if err := m.saveSlot(prof, p, c); err != nil {
			return err
		}
		m.Reg.Active[p.Name()] = prof
		m.logf("%s: saved %s as %q", p.Name(), id, prof)
	}
	if !found {
		m.logf("no logged-in accounts found")
	}
	return m.Reg.Save()
}

// Add logs a new account in inside an isolated home, so the current global
// login is never touched, then stores it in prof's slot.
func (m *Manager) Add(p provider.Provider, prof string, stdin io.Reader, stdout, stderr io.Writer) error {
	if _, err := exec.LookPath(p.Binary()); err != nil {
		return fmt.Errorf("%s is not installed", p.Binary())
	}
	home := m.IsolatedHome(prof, p)
	if err := provider.LinkShared(p, home); err != nil {
		return err
	}
	cmd := p.LoginCmd(home)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	m.logf("Logging in to %s for profile %q (your current login is not affected)…", p.Name(), prof)
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return err
		}
	}
	c, err := p.Capture(home)
	if errors.Is(err, provider.ErrNotLoggedIn) {
		return fmt.Errorf("login did not complete for %s", p.Name())
	}
	if err != nil {
		return err
	}
	id := p.Identity(c)
	if owner, ok := m.Reg.Owner(p.Name(), id); ok && owner != prof {
		m.logf("⚠ %s is also saved in profile %q; keep one copy per account (`ai rm %s %s`)", id, owner, owner, p.Name())
	}
	if err := m.saveSlot(prof, p, c); err != nil {
		return err
	}
	m.logf("%s: saved %s as %q", p.Name(), id, prof)
	return m.Reg.Save()
}

// PrepareIsolated readies prof's isolated home for p and returns it.
func (m *Manager) PrepareIsolated(p provider.Provider, prof string) (string, error) {
	if _, ok := m.Reg.Identity(prof, p.Name()); !ok {
		return "", fmt.Errorf("profile %q has no %s account (add one: ai add %s %s)", prof, p.Name(), p.Name(), prof)
	}
	home := m.IsolatedHome(prof, p)
	if err := provider.LinkShared(p, home); err != nil {
		return "", err
	}
	if err := m.Sync(prof, p); err != nil {
		return "", err
	}
	if _, err := p.Capture(home); errors.Is(err, provider.ErrNotLoggedIn) {
		c, err := m.slot(prof, p)
		if err != nil {
			return "", err
		}
		if err := p.Apply(home, c); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	return home, nil
}

// Run launches p with prof's account in its isolated home and syncs the
// (possibly refreshed) token back afterwards. Returns the child's exit code.
func (m *Manager) Run(p provider.Provider, prof string, args []string) (int, error) {
	home, err := m.PrepareIsolated(p, prof)
	if err != nil {
		return 1, err
	}
	if err := m.Reg.Save(); err != nil {
		return 1, err
	}
	cmd := exec.Command(p.Binary(), args...)
	cmd.Env = append(os.Environ(), p.Env(home)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()
	code := 0
	if runErr != nil {
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) {
			return 1, runErr
		}
		code = ee.ExitCode()
	}
	if err := m.Sync(prof, p); err != nil {
		return code, err
	}
	return code, m.Reg.Save()
}

// Env returns env assignments that point every provider in prof at its
// isolated home, for `eval "$(ai env work)"`.
func (m *Manager) Env(prof string) ([]string, error) {
	if _, ok := m.Reg.Profiles[prof]; !ok {
		return nil, fmt.Errorf("no profile %q", prof)
	}
	var env []string
	for _, p := range m.Providers {
		if _, ok := m.Reg.Identity(prof, p.Name()); !ok {
			continue
		}
		home, err := m.PrepareIsolated(p, prof)
		if err != nil {
			return nil, err
		}
		env = append(env, p.Env(home)...)
	}
	return env, m.Reg.Save()
}

// Remove deletes prof's slot for p (or all providers when p is nil).
func (m *Manager) Remove(prof string, p provider.Provider) error {
	if _, ok := m.Reg.Profiles[prof]; !ok {
		return fmt.Errorf("no profile %q", prof)
	}
	ps := m.Providers
	if p != nil {
		ps = []provider.Provider{p}
	}
	for _, p := range ps {
		if _, ok := m.Reg.Identity(prof, p.Name()); !ok {
			continue
		}
		home := m.IsolatedHome(prof, p)
		if cl, ok := p.(interface{ Forget(home string) error }); ok {
			if err := cl.Forget(home); err != nil && !errors.Is(err, secret.ErrNotFound) {
				return err
			}
		}
		if err := os.RemoveAll(home); err != nil {
			return err
		}
		if err := m.Store.Delete(slotService, prof+"/"+p.Name()); err != nil && !errors.Is(err, secret.ErrNotFound) {
			return err
		}
		m.Reg.Unset(prof, p.Name())
		m.logf("removed %s from %q", p.Name(), prof)
	}
	os.Remove(filepath.Join(m.Root, "profiles", prof))
	return m.Reg.Save()
}

// Current reports the live global identity per provider.
func (m *Manager) Current() map[string]string {
	out := map[string]string{}
	for _, p := range m.Providers {
		c, err := p.Capture("")
		switch {
		case errors.Is(err, provider.ErrNotLoggedIn):
			out[p.Name()] = "(not logged in)"
		case err != nil:
			out[p.Name()] = "(error: " + err.Error() + ")"
		default:
			out[p.Name()] = p.Identity(c)
		}
	}
	return out
}

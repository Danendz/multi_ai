// Package profile persists the non-secret profile index: which account
// (identity) sits in each profile's provider slot, and which profile is
// globally active per provider. Tokens themselves live in the Keychain.
package profile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

type Registry struct {
	path string
	// profile -> provider -> identity
	Profiles map[string]map[string]string `json:"profiles"`
	// provider -> profile
	Active map[string]string `json:"active"`
}

func Load(path string) (*Registry, error) {
	r := &Registry{path: path, Profiles: map[string]map[string]string{}, Active: map[string]string{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, r); err != nil {
		return nil, err
	}
	if r.Profiles == nil {
		r.Profiles = map[string]map[string]string{}
	}
	if r.Active == nil {
		r.Active = map[string]string{}
	}
	return r, nil
}

func (r *Registry) Exists() bool {
	_, err := os.Stat(r.path)
	return err == nil
}

func (r *Registry) Save() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

func (r *Registry) Names() []string {
	n := make([]string, 0, len(r.Profiles))
	for k := range r.Profiles {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

func (r *Registry) Identity(profile, provider string) (string, bool) {
	id, ok := r.Profiles[profile][provider]
	return id, ok
}

func (r *Registry) Set(profile, provider, identity string) {
	if r.Profiles[profile] == nil {
		r.Profiles[profile] = map[string]string{}
	}
	r.Profiles[profile][provider] = identity
}

func (r *Registry) Unset(profile, provider string) {
	delete(r.Profiles[profile], provider)
	if len(r.Profiles[profile]) == 0 {
		delete(r.Profiles, profile)
	}
	if r.Active[provider] == profile {
		delete(r.Active, provider)
	}
}

// Owner returns the profile holding identity for provider, if any.
func (r *Registry) Owner(provider, identity string) (string, bool) {
	for _, name := range r.Names() {
		if r.Profiles[name][provider] == identity {
			return name, true
		}
	}
	return "", false
}

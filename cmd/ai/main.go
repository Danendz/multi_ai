// Command ai switches between accounts of AI coding CLIs (Claude Code, Codex,
// Gemini) without logging out.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/Danendz/multi_ai/internal/provider"
	"github.com/Danendz/multi_ai/internal/secret"
	"github.com/Danendz/multi_ai/internal/swap"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		var ec exitCode
		if errors.As(err, &ec) {
			os.Exit(int(ec))
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func manager() (*swap.Manager, error) {
	deps, err := provider.DefaultDeps()
	if err != nil {
		return nil, err
	}
	root := os.Getenv("AI_SWITCH_HOME")
	if root == "" {
		root = filepath.Join(deps.HomeDir, ".ai-switch")
	}
	return swap.New(root, secret.Keychain{}, deps, os.Stdout)
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "ai",
		Short:         "Switch Claude Code / Codex / Gemini accounts without logging out",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE:          func(cmd *cobra.Command, _ []string) error { return pickAndSwitch() },
	}
	root.AddCommand(switchCmd(), addCmd(), importCmd(), runCmd(), envCmd(), lsCmd(), currentCmd(), rmCmd())
	return root
}

func switchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "switch [profile] | switch <provider> <profile>",
		Short: "Make a profile the global login (all providers, or just one)",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := manager()
			if err != nil {
				return err
			}
			switch len(args) {
			case 0:
				return pickAndSwitch()
			case 1:
				return m.SwitchProfile(args[0])
			}
			p, err := m.Provider(args[0])
			if err != nil {
				return err
			}
			return m.Switch(p, args[1])
		},
	}
}

func addCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <provider> <profile>",
		Short: "Log in a new account into a profile (your current login is untouched)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := manager()
			if err != nil {
				return err
			}
			p, err := m.Provider(args[0])
			if err != nil {
				return err
			}
			return m.Add(p, args[1], os.Stdin, os.Stdout, os.Stderr)
		},
	}
}

func importCmd() *cobra.Command {
	var only []string
	c := &cobra.Command{
		Use:   "import <profile>",
		Short: "Save the accounts you're currently logged into as a profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := manager()
			if err != nil {
				return err
			}
			var ps []provider.Provider
			for _, n := range only {
				p, err := m.Provider(n)
				if err != nil {
					return err
				}
				ps = append(ps, p)
			}
			return m.Import(args[0], ps)
		},
	}
	c.Flags().StringSliceVarP(&only, "provider", "p", nil, "only these providers")
	return c
}

func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run <provider> <profile> [-- args...]",
		Short: "Launch a tool with a profile's account in isolation (side-by-side accounts)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := manager()
			if err != nil {
				return err
			}
			p, err := m.Provider(args[0])
			if err != nil {
				return err
			}
			code, err := m.Run(p, args[1], args[2:])
			if err != nil {
				return err
			}
			if code != 0 {
				return exitCode(code)
			}
			return nil
		},
	}
}

func envCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "env <profile>",
		Short: `Print exports for this shell only: eval "$(ai env work)"`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := manager()
			if err != nil {
				return err
			}
			m.Out = os.Stderr
			env, err := m.Env(args[0])
			if err != nil {
				return err
			}
			for _, kv := range env {
				k, v, _ := strings.Cut(kv, "=")
				fmt.Printf("export %s='%s'\n", k, strings.ReplaceAll(v, "'", `'\''`))
			}
			return nil
		},
	}
}

func lsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List profiles and their accounts",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := manager()
			if err != nil {
				return err
			}
			if len(m.Reg.Profiles) == 0 {
				fmt.Println("no profiles yet — run `ai` to import your current logins")
				return nil
			}
			for _, name := range m.Reg.Names() {
				fmt.Println(name)
				slots := m.Reg.Profiles[name]
				provs := make([]string, 0, len(slots))
				for pn := range slots {
					provs = append(provs, pn)
				}
				sort.Strings(provs)
				for _, pn := range provs {
					mark := " "
					if m.Reg.Active[pn] == name {
						mark = "*"
					}
					fmt.Printf("  %s %-7s %s\n", mark, pn, slots[pn])
				}
			}
			return nil
		},
	}
}

func currentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show which account each tool is logged into right now",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := manager()
			if err != nil {
				return err
			}
			cur := m.Current()
			for _, p := range m.Providers {
				prof := m.Reg.Active[p.Name()]
				if prof != "" {
					prof = " [" + prof + "]"
				}
				fmt.Printf("%-7s %s%s\n", p.Name(), cur[p.Name()], prof)
			}
			return nil
		},
	}
}

func rmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <profile> [provider]",
		Short: "Forget a profile (or one provider's account in it)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := manager()
			if err != nil {
				return err
			}
			var p provider.Provider
			if len(args) == 2 {
				if p, err = m.Provider(args[1]); err != nil {
					return err
				}
			}
			return m.Remove(args[0], p)
		},
	}
}

// pickAndSwitch is the zero-argument UX: first run imports current logins,
// afterwards it's an arrow-key picker over profiles.
func pickAndSwitch() error {
	m, err := manager()
	if err != nil {
		return err
	}
	if len(m.Reg.Profiles) == 0 {
		name := "personal"
		if err := huh.NewInput().
			Title("Welcome! Name a profile for the accounts you're logged into now").
			Value(&name).Run(); err != nil {
			return err
		}
		if err := m.Import(strings.TrimSpace(name), nil); err != nil {
			return err
		}
		fmt.Println("\nAdd another account with: ai add <claude|codex|gemini> <profile>")
		return nil
	}
	var opts []huh.Option[string]
	for _, name := range m.Reg.Names() {
		var parts []string
		active := false
		for _, p := range m.Providers {
			if id, ok := m.Reg.Identity(name, p.Name()); ok {
				parts = append(parts, p.Name()+": "+id)
				active = active || m.Reg.Active[p.Name()] == name
			}
		}
		label := fmt.Sprintf("%s  %s", name, strings.Join(parts, " · "))
		if active {
			label += "  (active)"
		}
		opts = append(opts, huh.NewOption(label, name))
	}
	var choice string
	if err := huh.NewSelect[string]().Title("Switch to profile").Options(opts...).Value(&choice).Run(); err != nil {
		return err
	}
	return m.SwitchProfile(choice)
}

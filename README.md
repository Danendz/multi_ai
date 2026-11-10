# ai — switch AI CLI accounts without logging out

Keeps every Claude Code / Codex / Gemini CLI account logged in and swaps between them instantly.

```sh
go build -o ~/bin/ai ./cmd/ai

ai                         # first run: save current logins as a profile; afterwards: pick a profile
ai add claude work         # log in another account (current login untouched)
ai switch work             # make "work" the global login for every tool it has
ai switch codex personal   # switch just one tool
ai run claude work         # run with "work" in isolation, side by side with the global account
eval "$(ai env work)"      # point this shell only at "work"
ai limits [profile|provider]  # 5h / weekly usage per account (Claude, Codex)
ai ls | ai current | ai rm <profile> [provider]
```

## How it works
- Profiles hold one account per tool. Tokens are stored in the macOS Keychain (`ai-switch` / `<profile>/<provider>`); the index lives in `~/.ai-switch/config.json`.
- **Global switch** rewrites the tool's own login (Claude: Keychain `Claude Code-credentials` → `claudeAiOauth` + `oauthAccount` in `~/.claude.json`; Codex: `~/.codex/auth.json`; Gemini: `~/.gemini/oauth_creds.json` + `google_accounts.json`). MCP tokens and all other state are preserved.
- **Isolated mode** uses `~/.ai-switch/profiles/<profile>/<provider>` via `CLAUDE_CONFIG_DIR` / `CODEX_HOME` / `GEMINI_CLI_HOME`; settings, skills, plugins etc. are symlinked from your main config.
- **Limits** come from the same endpoints the tools' own `/usage` / `/status` use (Claude: `api.anthropic.com/api/oauth/usage`; Codex: `chatgpt.com/backend-api/wham/usage`). An expired access token is refreshed first and the rotated token is written back to every copy of that account.
- Refresh tokens rotate, so every switch/run first syncs the freshest copy of an account everywhere it lives (slot, global, isolated). An unknown logged-in account is never dropped: it is saved as a profile named after its email.

## Caveats
- macOS only. Codex must use the file credential store (default).
- Restart running sessions after a global switch — they keep the old account in memory.
- Gemini support is untested (CLI not available during development).

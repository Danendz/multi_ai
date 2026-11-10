package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type codexTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

// Limits reads the same data as Codex's /status rate-limit lines.
func (c *Codex) Limits(ctx context.Context, cr Creds) ([]Window, Creds, error) {
	var a struct {
		APIKey *string      `json:"OPENAI_API_KEY"`
		Tokens *codexTokens `json:"tokens"`
	}
	if err := json.Unmarshal(cr, &a); err != nil {
		return nil, nil, fmt.Errorf("invalid codex creds")
	}
	if a.Tokens == nil || a.Tokens.AccessToken == "" {
		if a.APIKey != nil {
			return nil, nil, fmt.Errorf("API key login has no plan limits")
		}
		return nil, nil, fmt.Errorf("invalid codex creds")
	}
	t := *a.Tokens
	var fresh Creds
	if exp, _ := jwtClaims(t.AccessToken)["exp"].(float64); time.Now().Add(expirySkew).After(time.Unix(int64(exp), 0)) {
		var err error
		if fresh, t, err = c.refresh(ctx, cr, t); err != nil {
			return nil, nil, err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codexUsageURL, nil)
	if err != nil {
		return nil, fresh, err
	}
	req.Header.Set("Authorization", "Bearer "+t.AccessToken)
	if t.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", t.AccountID)
	}
	req.Header.Set("User-Agent", "codex-cli")
	var u codexUsage
	if err := c.d.doJSON(req, &u); err != nil {
		return nil, fresh, err
	}
	return u.windows(), fresh, nil
}

// refresh trades the refresh token for new tokens and bumps last_refresh (our
// Freshness signal), leaving every other auth.json field intact.
func (c *Codex) refresh(ctx context.Context, cr Creds, t codexTokens) (Creds, codexTokens, error) {
	var r struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	err := c.d.postJSON(ctx, codexTokenURL, map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": t.RefreshToken,
	}, &r)
	if err != nil {
		return nil, t, fmt.Errorf("refresh token: %w", err)
	}
	if r.AccessToken == "" {
		return nil, t, fmt.Errorf("refresh token: empty access token")
	}
	auth := map[string]json.RawMessage{}
	tokens := map[string]json.RawMessage{}
	if err := json.Unmarshal(cr, &auth); err != nil {
		return nil, t, err
	}
	if err := json.Unmarshal(auth["tokens"], &tokens); err != nil {
		return nil, t, err
	}
	t.AccessToken = r.AccessToken
	setJSON(tokens, "access_token", r.AccessToken)
	if r.IDToken != "" {
		t.IDToken = r.IDToken
		setJSON(tokens, "id_token", r.IDToken)
	}
	if r.RefreshToken != "" {
		t.RefreshToken = r.RefreshToken
		setJSON(tokens, "refresh_token", r.RefreshToken)
	}
	setJSON(auth, "tokens", tokens)
	setJSON(auth, "last_refresh", time.Now().UTC().Format(time.RFC3339Nano))
	out, err := json.MarshalIndent(auth, "", "  ")
	return out, t, err
}

type codexWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type codexRateLimit struct {
	Primary   *codexWindow `json:"primary_window"`
	Secondary *codexWindow `json:"secondary_window"`
}

type codexUsage struct {
	RateLimit  *codexRateLimit `json:"rate_limit"`
	Additional []struct {
		LimitName string          `json:"limit_name"`
		RateLimit *codexRateLimit `json:"rate_limit"`
	} `json:"additional_rate_limits"`
}

func (u codexUsage) windows() []Window {
	now := time.Now()
	var ws []Window
	add := func(prefix string, rl *codexRateLimit) {
		if rl == nil {
			return
		}
		for _, w := range []*codexWindow{rl.Primary, rl.Secondary} {
			if w == nil {
				continue
			}
			var reset time.Time
			switch {
			case w.ResetAt > 0:
				reset = time.Unix(w.ResetAt, 0)
			case w.ResetAfterSeconds > 0:
				reset = now.Add(time.Duration(w.ResetAfterSeconds) * time.Second)
			}
			ws = append(ws, Window{Name: prefix + windowName(w.LimitWindowSeconds), Percent: w.UsedPercent, ResetsAt: reset})
		}
	}
	add("", u.RateLimit)
	for _, a := range u.Additional {
		add(a.LimitName+" ", a.RateLimit)
	}
	return ws
}

func windowName(secs int64) string {
	d := time.Duration(secs) * time.Second
	switch {
	case d == 7*24*time.Hour:
		return "week"
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d > 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return "window"
}

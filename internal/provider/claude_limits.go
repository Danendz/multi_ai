package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Used when a stored login predates Claude Code saving its scopes.
const claudeDefaultScopes = "user:profile user:inference user:sessions:claude_code user:mcp_servers"

type claudeOAuth struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	ExpiresAt    float64  `json:"expiresAt"`
	Scopes       []string `json:"scopes"`
}

// Limits reads the same data as Claude Code's /usage screen.
func (c *Claude) Limits(ctx context.Context, cr Creds) ([]Window, Creds, error) {
	var cc claudeCreds
	var o claudeOAuth
	if json.Unmarshal(cr, &cc) != nil || json.Unmarshal(cc.OAuth, &o) != nil || o.AccessToken == "" {
		return nil, nil, fmt.Errorf("invalid claude creds")
	}
	var fresh Creds
	if time.Now().Add(expirySkew).After(msTime(o.ExpiresAt)) {
		var err error
		if fresh, o.AccessToken, err = c.refresh(ctx, cc, o); err != nil {
			return nil, nil, err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeUsageURL, nil)
	if err != nil {
		return nil, fresh, err
	}
	req.Header.Set("Authorization", "Bearer "+o.AccessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	var u claudeUsage
	if err := c.d.doJSON(req, &u); err != nil {
		return nil, fresh, err
	}
	return u.windows(), fresh, nil
}

// refresh trades the refresh token for a new access token, returning the
// updated creds with every other stored field left intact.
func (c *Claude) refresh(ctx context.Context, cc claudeCreds, o claudeOAuth) (Creds, string, error) {
	scope := strings.Join(o.Scopes, " ")
	if scope == "" {
		scope = claudeDefaultScopes
	}
	var r struct {
		AccessToken           string  `json:"access_token"`
		RefreshToken          string  `json:"refresh_token"`
		ExpiresIn             float64 `json:"expires_in"`
		RefreshTokenExpiresIn float64 `json:"refresh_token_expires_in"`
	}
	err := c.d.postJSON(ctx, claudeTokenURL, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": o.RefreshToken,
		"client_id":     claudeClientID,
		"scope":         scope,
	}, &r)
	if err != nil {
		return nil, "", fmt.Errorf("refresh token: %w", err)
	}
	if r.AccessToken == "" {
		return nil, "", fmt.Errorf("refresh token: empty access token")
	}
	oauth := map[string]json.RawMessage{}
	if err := json.Unmarshal(cc.OAuth, &oauth); err != nil {
		return nil, "", err
	}
	now := time.Now()
	setJSON(oauth, "accessToken", r.AccessToken)
	if r.RefreshToken != "" {
		setJSON(oauth, "refreshToken", r.RefreshToken)
	}
	setJSON(oauth, "expiresAt", now.Add(time.Duration(r.ExpiresIn*float64(time.Second))).UnixMilli())
	if r.RefreshTokenExpiresIn > 0 {
		setJSON(oauth, "refreshTokenExpiresAt", now.Add(time.Duration(r.RefreshTokenExpiresIn*float64(time.Second))).UnixMilli())
	}
	if cc.OAuth, err = json.Marshal(oauth); err != nil {
		return nil, "", err
	}
	out, err := json.Marshal(cc)
	return out, r.AccessToken, err
}

type claudeUsageWindow struct {
	Utilization *float64  `json:"utilization"`
	ResetsAt    time.Time `json:"resets_at"`
}

type claudeUsage struct {
	Limits []struct {
		Kind     string    `json:"kind"`
		Percent  float64   `json:"percent"`
		ResetsAt time.Time `json:"resets_at"`
		Scope    *struct {
			Model *struct {
				DisplayName string `json:"display_name"`
			} `json:"model"`
		} `json:"scope"`
	} `json:"limits"`
	// Older response shape, used when "limits" is absent.
	FiveHour     *claudeUsageWindow `json:"five_hour"`
	SevenDay     *claudeUsageWindow `json:"seven_day"`
	SevenDayOpus *claudeUsageWindow `json:"seven_day_opus"`
}

func (u claudeUsage) windows() []Window {
	var ws []Window
	for _, l := range u.Limits {
		name := l.Kind
		switch l.Kind {
		case "session":
			name = "5h"
		case "weekly_all":
			name = "week"
		case "weekly_scoped":
			name = "week"
			if l.Scope != nil && l.Scope.Model != nil && l.Scope.Model.DisplayName != "" {
				name += " " + l.Scope.Model.DisplayName
			}
		}
		ws = append(ws, Window{Name: name, Percent: l.Percent, ResetsAt: l.ResetsAt})
	}
	if len(ws) > 0 {
		return ws
	}
	for _, w := range []struct {
		name string
		w    *claudeUsageWindow
	}{{"5h", u.FiveHour}, {"week", u.SevenDay}, {"week Opus", u.SevenDayOpus}} {
		if w.w != nil && w.w.Utilization != nil {
			ws = append(ws, Window{Name: w.name, Percent: *w.w.Utilization, ResetsAt: w.w.ResetsAt})
		}
	}
	return ws
}

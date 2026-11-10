package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func respond(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}

func fakeJWT(claims string) string {
	return "h." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".s"
}

func TestClaudeLimitsUsesValidTokenWithoutRefresh(t *testing.T) {
	exp := time.Now().Add(time.Hour).UnixMilli()
	c := &Claude{Deps{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != claudeUsageURL {
			t.Fatalf("unexpected request to %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer at" || r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			t.Fatalf("bad headers %v", r.Header)
		}
		return respond(200, `{"limits":[
			{"kind":"session","percent":28,"resets_at":"2026-11-10T15:10:00+00:00"},
			{"kind":"weekly_all","percent":14,"resets_at":"2026-11-15T18:00:00+00:00"},
			{"kind":"weekly_scoped","percent":3,"resets_at":"2026-11-15T18:00:00+00:00","scope":{"model":{"display_name":"Opus"}}}]}`), nil
	})}}}
	cr := Creds(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"at","refreshToken":"rt","expiresAt":%d}}`, exp))
	ws, fresh, err := c.Limits(context.Background(), cr)
	if err != nil || fresh != nil {
		t.Fatalf("err=%v fresh=%s", err, fresh)
	}
	if len(ws) != 3 || ws[0].Name != "5h" || ws[0].Percent != 28 || ws[1].Name != "week" || ws[2].Name != "week Opus" {
		t.Fatalf("got %v", ws)
	}
	if !ws[0].ResetsAt.Equal(time.Date(2026, 11, 10, 15, 10, 0, 0, time.UTC)) {
		t.Fatalf("reset %v", ws[0].ResetsAt)
	}
}

func TestClaudeLimitsRefreshesExpiredTokenAndKeepsFields(t *testing.T) {
	c := &Claude{Deps{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case claudeTokenURL:
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["refresh_token"] != "rt-old" || body["scope"] != "user:inference user:profile" || body["client_id"] != claudeClientID {
				t.Fatalf("bad refresh body %v", body)
			}
			return respond(200, `{"access_token":"at-new","refresh_token":"rt-new","expires_in":3600}`), nil
		case claudeUsageURL:
			if r.Header.Get("Authorization") != "Bearer at-new" {
				t.Fatalf("usage called with old token")
			}
			return respond(200, `{"five_hour":{"utilization":50,"resets_at":"2026-11-10T15:10:00Z"},"seven_day":null}`), nil
		}
		t.Fatalf("unexpected request to %s", r.URL)
		return nil, nil
	})}}}
	cr := Creds(`{"claudeAiOauth":{"accessToken":"at-old","refreshToken":"rt-old","expiresAt":1000,"scopes":["user:inference","user:profile"],"subscriptionType":"max"},"oauthAccount":{"emailAddress":"a@b.dev"}}`)
	ws, fresh, err := c.Limits(context.Background(), cr)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 || ws[0].Name != "5h" || ws[0].Percent != 50 {
		t.Fatalf("got %v", ws)
	}
	var cc struct {
		OAuth struct {
			AccessToken, RefreshToken, SubscriptionType string
			ExpiresAt                                   int64
		} `json:"claudeAiOauth"`
	}
	json.Unmarshal(fresh, &cc)
	if cc.OAuth.AccessToken != "at-new" || cc.OAuth.RefreshToken != "rt-new" || cc.OAuth.SubscriptionType != "max" {
		t.Fatalf("fresh creds %s", fresh)
	}
	if c.Identity(fresh) != "a@b.dev" || !c.Freshness(fresh).After(time.Now()) {
		t.Fatalf("identity/freshness lost: %s", fresh)
	}
}

func TestClaudeLimitsDeadRefreshTokenNeedsReauth(t *testing.T) {
	c := &Claude{Deps{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return respond(400, `{"error":"invalid_grant"}`), nil
	})}}}
	_, fresh, err := c.Limits(context.Background(), Creds(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":1}}`))
	if fresh != nil || err == nil || !strings.Contains(err.Error(), ErrReauth.Error()) {
		t.Fatalf("fresh=%s err=%v", fresh, err)
	}
}

func TestClaudeLimitsReturnsRefreshedCredsEvenWhenUsageFails(t *testing.T) {
	c := &Claude{Deps{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == claudeTokenURL {
			return respond(200, `{"access_token":"at-new","refresh_token":"rt-new","expires_in":3600}`), nil
		}
		return respond(500, ``), nil
	})}}}
	_, fresh, err := c.Limits(context.Background(), Creds(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":1}}`))
	if err == nil || fresh == nil || !strings.Contains(string(fresh), "rt-new") {
		t.Fatalf("fresh=%s err=%v", fresh, err)
	}
}

func TestCodexLimitsRefreshesAndParsesWindows(t *testing.T) {
	newAT := fakeJWT(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix()))
	c := &Codex{Deps{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case codexTokenURL:
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["refresh_token"] != "rt-old" || body["grant_type"] != "refresh_token" {
				t.Fatalf("bad refresh body %v", body)
			}
			return respond(200, fmt.Sprintf(`{"access_token":%q,"refresh_token":"rt-new"}`, newAT)), nil
		case codexUsageURL:
			if r.Header.Get("Authorization") != "Bearer "+newAT || r.Header.Get("ChatGPT-Account-Id") != "acc-1" {
				t.Fatalf("bad headers %v", r.Header)
			}
			return respond(200, `{"plan_type":"plus","rate_limit":{"allowed":true,"limit_reached":false,
				"primary_window":{"used_percent":12,"limit_window_seconds":18000,"reset_after_seconds":600,"reset_at":1900000000},
				"secondary_window":{"used_percent":40,"limit_window_seconds":604800,"reset_after_seconds":6000,"reset_at":0}},
				"additional_rate_limits":[{"limit_name":"codex-spark","rate_limit":{"primary_window":{"used_percent":5,"limit_window_seconds":18000}}}]}`), nil
		}
		t.Fatalf("unexpected request to %s", r.URL)
		return nil, nil
	})}}}
	oldAT := fakeJWT(`{"exp":1000}`)
	idt := fakeJWT(`{"email":"c@d.dev"}`)
	cr := Creds(fmt.Sprintf(`{"OPENAI_API_KEY":null,"tokens":{"id_token":%q,"access_token":%q,"refresh_token":"rt-old","account_id":"acc-1"},"last_refresh":"2025-01-01T00:00:00Z"}`, idt, oldAT))
	ws, fresh, err := c.Limits(context.Background(), cr)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 3 || ws[0].Name != "5h" || ws[0].Percent != 12 || ws[1].Name != "week" || ws[2].Name != "codex-spark 5h" {
		t.Fatalf("got %v", ws)
	}
	if !ws[0].ResetsAt.Equal(time.Unix(1900000000, 0)) || time.Until(ws[1].ResetsAt) < 99*time.Minute {
		t.Fatalf("resets %v %v", ws[0].ResetsAt, ws[1].ResetsAt)
	}
	if !strings.Contains(string(fresh), "rt-new") || c.Identity(fresh) != "c@d.dev" || !c.Freshness(fresh).After(time.Now().Add(-time.Minute)) {
		t.Fatalf("fresh creds %s", fresh)
	}
}

func TestCodexLimitsAPIKeyLogin(t *testing.T) {
	c := &Codex{Deps{}}
	if _, _, err := c.Limits(context.Background(), Creds(`{"OPENAI_API_KEY":"sk-123456789"}`)); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("err=%v", err)
	}
}

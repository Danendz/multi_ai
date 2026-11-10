package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Window is one plan usage limit, e.g. the 5-hour or weekly window.
type Window struct {
	Name     string
	Percent  float64
	ResetsAt time.Time // zero if unknown
}

// Limiter is implemented by providers that can report plan usage limits.
//
// Limits may refresh an expired access token. Refresh tokens rotate, so when
// the returned Creds are non-nil they are the only working copy of the
// account and must be persisted — even if err is also non-nil.
type Limiter interface {
	Limits(ctx context.Context, c Creds) ([]Window, Creds, error)
}

// ErrReauth means the stored login was rejected and must be redone.
var ErrReauth = errors.New("login expired or revoked; log in again")

// Endpoints, overridable in tests.
var (
	claudeTokenURL = "https://platform.claude.com/v1/oauth/token"
	claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"
	codexTokenURL  = "https://auth.openai.com/oauth/token"
	codexUsageURL  = "https://chatgpt.com/backend-api/wham/usage"
)

const (
	claudeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	codexClientID  = "app_EMoamEEZ73f0CkXaXp7hrann"
	// Refresh a little early so the token can't expire mid-request.
	expirySkew = time.Minute
)

func (d Deps) client() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// doJSON sends req and decodes a 200 JSON response into out. 400/401/403 map
// to ErrReauth, since for these endpoints they mean a dead token.
func (d Deps) doJSON(req *http.Request, out any) error {
	resp, err := d.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		return json.Unmarshal(body, out)
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w (%s %s: HTTP %d)", ErrReauth, req.Method, req.URL.Host, resp.StatusCode)
	default:
		return fmt.Errorf("%s %s: HTTP %d", req.Method, req.URL.Host, resp.StatusCode)
	}
}

func (d Deps) postJSON(ctx context.Context, url string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return d.doJSON(req, out)
}

// setJSON stores v under key in a raw JSON object, preserving other fields.
func setJSON(m map[string]json.RawMessage, key string, v any) {
	b, _ := json.Marshal(v)
	m[key] = b
}

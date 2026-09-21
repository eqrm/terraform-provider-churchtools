package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ChurchTools' LEGACY master-data endpoint is not REST and is not in any
// OpenAPI spec. It is the only write path for some tables (Bereiche), and it
// needs a real SESSION, not the bearer-style token header the REST calls use:
//
//	1. GET /api/whoami?login_token=<token>  -> sets the session cookie
//	2. GET /api/csrftoken                   -> returns the CSRF token
//	3. POST /index.php?q=<module>/ajax      -> form-encoded, with cookie + CSRF-Token
//
// The login_token QUERY PARAM is required in step 1: ct-cli records that an
// Authorization header there yields a null CSRF token and breaks the handshake.

// session returns the cookie and CSRF token, performing the handshake once per
// Client. One Client is shared by every resource and Terraform applies
// resources concurrently, so the fields are both written and READ under the
// mutex -- returning copies keeps callers off the shared state entirely.
func (c *Client) session(ctx context.Context) (cookie, csrf string, err error) {
	// A session the operator supplied is already the answer, and buying another
	// is not an option: this client holds no token to buy one with. Returned
	// before the mutex because these fields are immutable after construction.
	if c.usesSession() {
		return c.fixedCookie, c.fixedCSRF, nil
	}

	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()

	if c.cookie != "" && c.csrfToken != "" {
		return c.cookie, c.csrfToken, nil
	}

	whoami := fmt.Sprintf("%s/api/whoami?login_token=%s", c.baseURL, url.QueryEscape(c.token))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, whoami, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("churchtools: session handshake (whoami): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("churchtools: session handshake (whoami) returned %d: %s", resp.StatusCode, string(body))
	}

	var jar []string
	for _, ck := range resp.Cookies() {
		jar = append(jar, ck.Name+"="+ck.Value)
	}
	if len(jar) == 0 {
		return "", "", fmt.Errorf("churchtools: session handshake (whoami) set no cookie — is the token valid?")
	}
	c.cookie = strings.Join(jar, "; ")

	// The CSRF read must carry the cookie just obtained.
	raw, err := c.doWithCookie(ctx, http.MethodGet, "/csrftoken")
	if err != nil {
		c.cookie = ""
		return "", "", fmt.Errorf("churchtools: session handshake (csrftoken): %w", err)
	}
	var token string
	if err := unwrap(raw, &token); err != nil {
		c.cookie = ""
		return "", "", fmt.Errorf("churchtools: decoding csrftoken: %w", err)
	}
	if token == "" {
		c.cookie = ""
		return "", "", fmt.Errorf("churchtools: session handshake returned an empty CSRF token")
	}
	c.csrfToken = token
	return c.cookie, c.csrfToken, nil
}

// Ajax calls the legacy form-encoded endpoint and discards the payload.
func (c *Client) Ajax(ctx context.Context, module string, params map[string]string) error {
	return c.AjaxJSON(ctx, module, params, nil)
}

// AjaxJSON calls the legacy form-encoded endpoint and, when `into` is non-nil,
// decodes the `data` payload into it.
//
// Unlike REST, a failure here is a 200 carrying {"status":"error"} — the status
// code alone proves nothing, so the envelope is what decides. That is also how
// the endpoint can be trusted to validate function names rather than silently
// ignoring an unknown one.
func (c *Client) AjaxJSON(ctx context.Context, module string, params map[string]string, into any) error {
	if c == nil {
		return fmt.Errorf("%w: %s/ajax %s", ErrNotConfigured, module, params["func"])
	}
	cookie, csrf, err := c.session(ctx)
	if err != nil {
		return err
	}

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}

	endpoint := fmt.Sprintf("%s/index.php?q=%s/ajax", c.baseURL, module)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("CSRF-Token", csrf)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var env struct {
		Status  string          `json:"status"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	decodeErr := json.Unmarshal(raw, &env)

	// The status code is checked BEFORE the envelope, because an expired session
	// does not get one. A signed-out ChurchTools answers this endpoint with a 401
	// carrying an HTML login page, which decodes into nothing and would otherwise
	// surface as "undecodable response: <!DOCTYPE html>" -- true, and useless.
	// Departments are written only through here, so without this the remedy the
	// REST path gives never reaches the one resource that needs the legacy one.
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		detail := strings.TrimSpace(env.Message)
		if detail == "" {
			detail = strings.TrimSpace(string(raw))
		}
		if resp.StatusCode == http.StatusUnauthorized && c.usesSession() {
			return fmt.Errorf("%w (%s/ajax %s): %s. Sessions expire; re-run so the credential "+
				"helper fetches a fresh one (e.g. `ct auth token`). The provider cannot renew a "+
				"session it was handed", ErrSessionExpired, module, params["func"], detail)
		}
		return fmt.Errorf("churchtools: %s/ajax %s returned %d: %s",
			module, params["func"], resp.StatusCode, detail)
	}

	if decodeErr != nil {
		return fmt.Errorf("churchtools: %s/ajax %s: undecodable response: %s",
			module, params["func"], string(raw))
	}
	if env.Status != "success" {
		msg := env.Message
		if msg == "" {
			msg = string(raw)
		}
		return fmt.Errorf("churchtools: %s/ajax %s failed: %s", module, params["func"], msg)
	}
	if into == nil || len(env.Data) == 0 {
		return nil
	}
	return json.Unmarshal(env.Data, into)
}

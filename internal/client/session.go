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

// ensureSession performs the handshake once and caches cookie + CSRF token.
func (c *Client) ensureSession(ctx context.Context) error {
	if c.cookie != "" && c.csrfToken != "" {
		return nil
	}

	whoami := fmt.Sprintf("%s/api/whoami?login_token=%s", c.baseURL, url.QueryEscape(c.token))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, whoami, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("churchtools: session handshake (whoami): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("churchtools: session handshake (whoami) returned %d: %s", resp.StatusCode, string(body))
	}

	var jar []string
	for _, ck := range resp.Cookies() {
		jar = append(jar, ck.Name+"="+ck.Value)
	}
	if len(jar) == 0 {
		return fmt.Errorf("churchtools: session handshake (whoami) set no cookie — is the token valid?")
	}
	c.cookie = strings.Join(jar, "; ")

	// The CSRF read must carry the cookie just obtained.
	raw, err := c.doWithCookie(ctx, http.MethodGet, "/csrftoken", nil)
	if err != nil {
		return fmt.Errorf("churchtools: session handshake (csrftoken): %w", err)
	}
	var token string
	if err := unwrap(raw, &token); err != nil {
		return fmt.Errorf("churchtools: decoding csrftoken: %w", err)
	}
	if token == "" {
		return fmt.Errorf("churchtools: session handshake returned an empty CSRF token")
	}
	c.csrfToken = token
	return nil
}

// Ajax calls the legacy form-encoded endpoint.
//
// Unlike REST, a failure here is a 200 carrying {"status":"error"} — the status
// code alone proves nothing, so the envelope is what decides. That is also how
// the endpoint can be trusted to validate function names rather than silently
// ignoring an unknown one.
func (c *Client) Ajax(ctx context.Context, module string, params map[string]string) error {
	if err := c.ensureSession(ctx); err != nil {
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
	req.Header.Set("Cookie", c.cookie)
	req.Header.Set("CSRF-Token", c.csrfToken)
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
	if err := json.Unmarshal(raw, &env); err != nil {
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
	return nil
}

// AjaxJSON is Ajax for the calls whose `data` payload is needed.
func (c *Client) AjaxJSON(ctx context.Context, module string, params map[string]string, into any) error {
	if err := c.ensureSession(ctx); err != nil {
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
	req.Header.Set("Cookie", c.cookie)
	req.Header.Set("CSRF-Token", c.csrfToken)
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
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("churchtools: %s/ajax %s: undecodable response: %s", module, params["func"], string(raw))
	}
	if env.Status != "success" {
		msg := env.Message
		if msg == "" {
			msg = string(raw)
		}
		return fmt.Errorf("churchtools: %s/ajax %s failed: %s", module, params["func"], msg)
	}
	if len(env.Data) == 0 {
		return nil
	}
	return json.Unmarshal(env.Data, into)
}

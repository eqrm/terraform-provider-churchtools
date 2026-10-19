package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ChurchTools' LEGACY master-data endpoint is not REST and is not in any
// OpenAPI spec. It is the only write path for some tables (Bereiche), and it
// needs a real SESSION, not the bearer-style token header the REST calls use:
//
// The same session now also authenticates the REST calls in token mode (see
// Client.credentials), so a run performs exactly one login.
//
//	1. GET /api/whoami?login_token=<token>  -> sets the session cookie
//	2. GET /api/csrftoken                   -> returns the CSRF token
//	3. POST /index.php?q=<module>/ajax      -> form-encoded, with cookie + CSRF-Token
//
// The login_token QUERY PARAM is required in step 1: ct-cli records that an
// Authorization header there yields a null CSRF token and breaks the handshake.

// Handshake limits. ChurchTools answers an over-eager login with 429; waiting
// is right, but bounded so a stuck host fails the run instead of hanging it.
const (
	handshakeAttempts = 4
	handshakeMaxWait  = 30 * time.Second
	handshakeBackoff  = time.Second
)

// session returns the cookie and CSRF token, performing the handshake once per
// Client. One Client is shared by every resource and Terraform applies
// resources concurrently, so the fields are both written and READ under the
// mutex -- returning copies keeps callers off the shared state entirely. The
// mutex is held across the handshake, so concurrent first callers wait for the
// one login instead of each making their own.
func (c *Client) session(ctx context.Context) (cookie, csrf string, err error) {
	// A session the operator supplied is already the answer, and buying another
	// is not an option: this client holds no token to buy one with. Returned
	// before the mutex because these fields are immutable after construction.
	if c.usesSession() {
		return c.fixedCookie, c.fixedCSRF, nil
	}

	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if c.handshakeErr != nil {
		return "", "", c.handshakeErr
	}
	if c.cookie != "" && c.csrfToken != "" {
		return c.cookie, c.csrfToken, nil
	}
	return c.handshakeLocked(ctx)
}

// renewSession replaces a session ChurchTools rejected. `stale` is the cookie
// the caller was refused with: if another goroutine has already renewed, the
// fresh session is returned without a second login.
func (c *Client) renewSession(ctx context.Context, stale string) (cookie, csrf string, err error) {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if c.cookie != "" && c.csrfToken != "" && c.cookie != stale {
		return c.cookie, c.csrfToken, nil
	}
	c.cookie, c.csrfToken, c.handshakeErr = "", "", nil
	return c.handshakeLocked(ctx)
}

// handshakeLocked buys a session. sessionMu must be held.
func (c *Client) handshakeLocked(ctx context.Context) (string, string, error) {
	cookie, csrf, err := c.handshake(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.handshakeErr = err
		}
		return "", "", err
	}
	c.cookie, c.csrfToken = cookie, csrf
	return cookie, csrf, nil
}

func (c *Client) handshake(ctx context.Context) (string, string, error) {
	cookie, err := c.whoami(ctx)
	if err != nil {
		return "", "", err
	}

	// The CSRF read carries the cookie just obtained and nothing else: an
	// Authorization header here would be a second login.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/csrftoken", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cookie", cookie)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("churchtools: session handshake (csrftoken): %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", "", fmt.Errorf("churchtools: session handshake (csrftoken): %w",
			&StatusError{Method: http.MethodGet, Path: "/csrftoken", Status: resp.StatusCode, Body: string(raw)})
	}
	var token string
	if err := unwrap(raw, &token); err != nil {
		return "", "", fmt.Errorf("churchtools: decoding csrftoken: %w", err)
	}
	if token == "" {
		return "", "", fmt.Errorf("churchtools: session handshake returned an empty CSRF token")
	}
	return cookie, token, nil
}

// whoami performs the login (the ONE login of a run) and returns the cookie.
// It retries 429 within bounds, honouring Retry-After.
func (c *Client) whoami(ctx context.Context) (string, error) {
	target := fmt.Sprintf("%s/api/whoami?login_token=%s", c.baseURL, url.QueryEscape(c.token))
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return "", redactErr(err)
		}
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			return "", fmt.Errorf("churchtools: session handshake (whoami): %w", redactErr(err))
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests {
			if attempt >= handshakeAttempts {
				return "", fmt.Errorf("churchtools: session handshake (whoami) was rate-limited "+
					"(429) %d times in a row; ChurchTools is refusing logins, try again later: %w",
					attempt, &StatusError{Method: http.MethodGet, Path: "/whoami", Status: resp.StatusCode, Body: string(body)})
			}
			wait := retryAfter(resp.Header.Get("Retry-After"), handshakeBackoff<<(attempt-1))
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return "", ctx.Err()
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return "", fmt.Errorf("churchtools: session handshake (whoami): %w",
				&StatusError{Method: http.MethodGet, Path: "/whoami", Status: resp.StatusCode, Body: string(body)})
		}
		var jar []string
		for _, ck := range resp.Cookies() {
			jar = append(jar, ck.Name+"="+ck.Value)
		}
		if len(jar) == 0 {
			return "", fmt.Errorf("churchtools: session handshake (whoami) set no cookie — is the token valid?")
		}
		return strings.Join(jar, "; "), nil
	}
}

// retryAfter reads a Retry-After header (seconds or HTTP date), falling back to
// `def`, and caps the result.
func retryAfter(h string, def time.Duration) time.Duration {
	d := def
	if h = strings.TrimSpace(h); h != "" {
		if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
			d = time.Duration(secs) * time.Second
		} else if t, err := http.ParseTime(h); err == nil {
			d = time.Until(t)
		}
	}
	if d < 0 {
		d = 0
	}
	if d > handshakeMaxWait {
		d = handshakeMaxWait
	}
	return d
}

// redactErr strips the query string from a *url.Error. The login token travels
// as a query parameter on /whoami, and net/http quotes the full URL in its
// errors, which would otherwise carry the token into Terraform diagnostics.
func redactErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		u := ue.URL
		if i := strings.IndexByte(u, '?'); i >= 0 {
			u = u[:i] + "?<redacted>"
		}
		return &url.Error{Op: ue.Op, URL: u, Err: ue.Err}
	}
	return err
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
	post := func(cookie, csrf string) (int, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Cookie", cookie)
		req.Header.Set("CSRF-Token", csrf)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := c.http.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		return resp.StatusCode, raw, err
	}
	status, raw, err := post(cookie, csrf)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized && !c.usesSession() {
		// Same rule as REST: renew once, retry once.
		cookie, csrf, err = c.renewSession(ctx, cookie)
		if err != nil {
			return err
		}
		if status, raw, err = post(cookie, csrf); err != nil {
			return err
		}
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
	if status < 200 || status > 299 {
		detail := strings.TrimSpace(env.Message)
		if detail == "" {
			detail = strings.TrimSpace(string(raw))
		}
		if status == http.StatusUnauthorized && c.usesSession() {
			return fmt.Errorf("%w (%s/ajax %s): %s. Sessions expire; re-run so the credential "+
				"helper fetches a fresh one (e.g. `ct auth token`). The provider cannot renew a "+
				"session it was handed", ErrSessionExpired, module, params["func"], detail)
		}
		return fmt.Errorf("churchtools: %s/ajax %s returned %d: %s",
			module, params["func"], status, detail)
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

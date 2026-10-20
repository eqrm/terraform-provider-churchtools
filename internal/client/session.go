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
	// transientLimit is how many consecutive handshakes may fail for a reason
	// that says nothing about the token (5xx, network) before the failure is
	// treated as the run's answer.
	transientLimit = 3
	// maxRenewals caps how many times one Client buys a replacement session.
	// A route that keeps answering 401 for a reason other than expiry would
	// otherwise cost one login per request, the very storm this client avoids.
	maxRenewals = 3
)

// ErrSessionKeepsExpiring is returned once the renewal budget is spent.
var ErrSessionKeepsExpiring = errors.New("churchtools: the session keeps expiring; not logging in again")

// definitiveErr marks a handshake failure that retrying cannot fix (the token
// is refused, logins are rate-limited past our patience). Only these are
// remembered for the run; a 502 or a dropped connection is not.
type definitiveErr struct{ error }

func (e definitiveErr) Unwrap() error { return e.error }

// lockSession takes the session lock, giving up if ctx ends first. It is a
// channel rather than a sync.Mutex so a waiter behind a slow handshake (a 429
// wait can take ~90s) still honours its own deadline.
func (c *Client) lockSession(ctx context.Context) error {
	select {
	case c.sessionSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) unlockSession() { <-c.sessionSem }

// session returns the cookie and CSRF token, performing the handshake once per
// Client. One Client is shared by every resource and Terraform applies
// resources concurrently, so the fields are both written and READ under the
// lock -- returning copies keeps callers off the shared state entirely. The
// lock is held across the handshake, so concurrent first callers wait for the
// one login instead of each making their own.
func (c *Client) session(ctx context.Context) (cookie, csrf string, err error) {
	// A session the operator supplied is already the answer, and buying another
	// is not an option: this client holds no token to buy one with. Returned
	// before the lock because these fields are immutable after construction.
	if c.usesSession() {
		return c.fixedCookie, c.fixedCSRF, nil
	}

	if err := c.lockSession(ctx); err != nil {
		return "", "", err
	}
	defer c.unlockSession()
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
// fresh session is returned without a second login. At most maxRenewals
// replacements are bought per Client.
func (c *Client) renewSession(ctx context.Context, stale string) (cookie, csrf string, err error) {
	if err := c.lockSession(ctx); err != nil {
		return "", "", err
	}
	defer c.unlockSession()
	if c.cookie != "" && c.csrfToken != "" && c.cookie != stale {
		return c.cookie, c.csrfToken, nil
	}
	if c.renewals >= maxRenewals {
		return "", "", fmt.Errorf("%w (%d renewals already used this run)", ErrSessionKeepsExpiring, c.renewals)
	}
	c.renewals++
	c.cookie, c.csrfToken, c.handshakeErr = "", "", nil
	return c.handshakeLocked(ctx)
}

// handshakeLocked buys a session. The session lock must be held.
func (c *Client) handshakeLocked(ctx context.Context) (string, string, error) {
	cookie, csrf, err := c.handshake(ctx)
	if err != nil {
		var de definitiveErr
		switch {
		case ctx.Err() != nil:
			// Our own cancellation says nothing about the host.
		case errors.As(err, &de):
			c.handshakeErr = err
		default:
			// Transient: the next caller retries, up to a bound.
			c.transientFails++
			if c.transientFails >= transientLimit {
				c.handshakeErr = err
			}
		}
		return "", "", err
	}
	c.transientFails = 0
	c.cookie, c.csrfToken = cookie, csrf
	return cookie, csrf, nil
}

func (c *Client) handshake(ctx context.Context) (string, string, error) {
	target := fmt.Sprintf("%s/api/whoami?login_token=%s", c.baseURL, url.QueryEscape(c.token))
	resp, _, err := c.handshakeGet(ctx, target, "", "/whoami")
	if err != nil {
		return "", "", fmt.Errorf("churchtools: session handshake (whoami): %w", err)
	}
	var jar []string
	for _, ck := range resp.Cookies() {
		jar = append(jar, ck.Name+"="+ck.Value)
	}
	if len(jar) == 0 {
		return "", "", definitiveErr{fmt.Errorf("churchtools: session handshake (whoami) set no cookie — is the token valid?")}
	}
	cookie := strings.Join(jar, "; ")

	// The CSRF read carries the cookie just obtained and nothing else: an
	// Authorization header here would be a second login.
	_, raw, err := c.handshakeGet(ctx, c.baseURL+"/api/csrftoken", cookie, "/csrftoken")
	if err != nil {
		return "", "", fmt.Errorf("churchtools: session handshake (csrftoken): %w", err)
	}
	var token string
	if err := unwrap(raw, &token); err != nil {
		return "", "", definitiveErr{fmt.Errorf("churchtools: decoding csrftoken: %w", err)}
	}
	if token == "" {
		return "", "", definitiveErr{fmt.Errorf("churchtools: session handshake returned an empty CSRF token")}
	}
	return cookie, token, nil
}

// handshakeGet is one GET of the handshake with the 429 policy: Retry-After
// honoured and capped, a few attempts, ctx-aware sleeps. `label` names the
// route in errors; `target` may carry the token, so it is never printed.
// Failures come back classified: 401/403, 4xx and an exhausted 429 are
// definitive; 5xx and transport errors are not.
func (c *Client) handshakeGet(ctx context.Context, target, cookie, label string) (*http.Response, []byte, error) {
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, nil, redactErr(err)
		}
		req.Header.Set("Accept", "application/json")
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, nil, redactErr(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		se := &StatusError{Method: http.MethodGet, Path: label, Status: resp.StatusCode, Body: string(body)}

		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			if attempt >= handshakeAttempts {
				return nil, nil, definitiveErr{fmt.Errorf("rate-limited (429) %d times in a row; "+
					"ChurchTools is refusing logins, try again later: %w", attempt, se)}
			}
			t := time.NewTimer(retryAfter(resp.Header.Get("Retry-After"), handshakeBackoff<<(attempt-1)))
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return nil, nil, ctx.Err()
			}
			continue
		case resp.StatusCode >= 200 && resp.StatusCode <= 299:
			return resp, body, nil
		case resp.StatusCode >= 500:
			return nil, nil, se
		default:
			return nil, nil, definitiveErr{se}
		}
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
	if !c.usesSession() && ajaxExpired(status, raw) {
		// Same rule as REST: renew once, retry once. A lapsed session is a 401
		// on some routes but, on this endpoint, a 200 carrying
		// {"status":"error","message":"Session expired!"} (seen on eqrm-dev).
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

// ajaxExpired reports whether a legacy-endpoint answer means the session
// lapsed: a 401, or the 200 {"status":"error","message":"Session expired!"}.
func ajaxExpired(status int, raw []byte) bool {
	if status == http.StatusUnauthorized {
		return true
	}
	var env struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if status < 200 || status > 299 || json.Unmarshal(raw, &env) != nil {
		return false
	}
	return env.Status == "error" && strings.Contains(strings.ToLower(env.Message), "session expired")
}

// Package client is the ChurchTools REST transport. It is deliberately
// untyped (Row = map[string]any) because the provider's per-resource schemas
// are the typing layer — mirroring how ct-cli's registry treats CT rows.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ErrNotFound reports that a specific row is gone, so a resource can be removed
// from state rather than failing the whole plan. It is produced ONLY for item
// operations that carry an id. A 404 on a collection path means the endpoint
// does not exist on this instance -- translating that into ErrNotFound would
// silently drop every resource of that type from state and re-create it on the
// next apply.
var ErrNotFound = errors.New("churchtools: resource not found")

// ErrNoID guards the empty-id trap: `GET /campuses/` + "" addresses the
// COLLECTION, which decodes as an array into a Row and leaves the resource
// permanently unreadable.
var ErrNoID = errors.New("churchtools: empty resource id")

// StatusError is any non-2xx response, kept structured so callers can tell a
// missing row from a missing endpoint.
type StatusError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("churchtools: %s %s returned %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// Row is one ChurchTools record as decoded from JSON.
type Row = map[string]any

type Client struct {
	baseURL string
	token   string
	http    *http.Client

	// A session supplied by the OPERATOR rather than bought with a token (see
	// NewWithSession). Written once at construction and never again, so unlike
	// the lazily acquired pair below these need no mutex -- and must not be
	// cleared on an error path, because nothing here can buy a replacement.
	fixedCookie string
	fixedCSRF   string

	// Legacy-endpoint session (see session.go). Acquired lazily: a run that
	// only touches REST resources never performs the handshake.
	//
	// One Client is shared by every resource (see provider.ProviderData) and
	// Terraform applies resources concurrently, so these two fields are written
	// from several goroutines and need the mutex.
	sessionMu sync.Mutex
	cookie    string
	csrfToken string

	// See LockMasterDataCreate.
	masterDataCreateMu sync.Mutex
}

// LockMasterDataCreate serializes the snapshot -> saveMasterData -> diff
// sequence a legacy Create uses to find its row, since saveMasterData returns
// no id. Terraform creates independent resources in parallel, and two such
// sequences that interleave each see both new rows and neither can claim one
// -- leaving both rows orphaned in ChurchTools. Call the returned func to
// unlock.
func (c *Client) LockMasterDataCreate() func() {
	c.masterDataCreateMu.Lock()
	return c.masterDataCreateMu.Unlock
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// NewWithSession builds a client that authenticates with a session somebody
// else already bought -- what `ct auth token` emits.
//
// The reason this mode exists is that the alternative is worse. A ChurchTools
// login token is permanent, cannot be scoped, cannot be rotated without
// invalidating every other use of it, and on a production instance it is an
// administrator credential. Writing one into a `.tfvars`, a CI variable or a
// `TF_LOG=DEBUG` transcript puts a forever-credential somewhere it will outlive
// whoever put it there. The session bought with it expires on its own, is
// dropped by `ct auth logout`, and a leaked copy is dead within hours.
//
// So the token stays in ct-cli's Keychain and never reaches this process.
func NewWithSession(baseURL, cookie, csrf string) *Client {
	return &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		fixedCookie: cookie,
		fixedCSRF:   csrf,
		http:        &http.Client{Timeout: 30 * time.Second},
	}
}

// usesSession reports whether this client was handed a session instead of a
// token. The two modes authenticate every REST call differently and, more
// importantly, recover from a 401 differently: a token client can buy a new
// session, a session client can only tell the operator to run again.
//
// EITHER half means session mode. Keying on the cookie alone would make a
// half-specified session fall back to token mode and send `Authorization:
// Login ` with an empty token -- a 401 whose message names neither cause. The
// provider refuses the partial pair at configure time, but this constructor is
// exported and is not obliged to be called through it.
func (c *Client) usesSession() bool { return c.fixedCookie != "" || c.fixedCSRF != "" }

// authenticate applies whichever credential this client holds.
//
// Token mode sends `Authorization: Login <token>`, which ChurchTools accepts on
// every REST route. Session mode sends the cookie instead and adds the CSRF
// header to writes -- exactly what a browser does, and what ct-cli has always
// done for its own REST calls once its handshake completed.
func (c *Client) authenticate(req *http.Request, method string) {
	if !c.usesSession() {
		req.Header.Set("Authorization", "Login "+c.token)
		return
	}
	req.Header.Set("Cookie", c.fixedCookie)
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("CSRF-Token", c.fixedCSRF)
	}
}

// ErrSessionExpired is a 401 in session mode.
//
// It is a distinct error because the remedy is distinct and not guessable: the
// provider cannot renew a session it was handed, so "check your credentials" is
// the wrong advice. Re-running is the right advice, because the credential
// helper fetches a fresh session on the next read.
var ErrSessionExpired = errors.New("churchtools: the supplied session is no longer valid")

// ErrNotConfigured reports a call on a client the provider never built.
//
// Configure DEFERS when any credential is unknown -- the normal state for the
// session pair, which arrives from a `data "external"` block -- and leaves
// ResourceData nil, so a resource can reach CRUD holding a nil client. Every
// request funnels through do() or AjaxJSON(), so checking the receiver in
// those two places turns a plugin crash into something an operator can read.
var ErrNotConfigured = errors.New("churchtools: client not configured (provider credentials were still unknown)")

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: %s %s", ErrNotConfigured, method, path)
	}
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("churchtools: encoding request body: %w", err)
		}
		buf = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/api"+path, buf)
	if err != nil {
		return nil, err
	}
	c.authenticate(req, method)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && c.usesSession() {
		// ct-cli calls its `expiresAt` a ceiling, not a promise: ChurchTools does
		// not advertise a session lifetime, so a 401 here is expected eventually
		// rather than exceptional. Say what to do about it.
		// Keep the instance's own words: not every 401 here is an expiry. A
		// malformed cookie, or one that never matched its CSRF token, 401s the
		// same way and re-running forever will not fix it.
		return nil, fmt.Errorf("%w (%s %s): %s. Sessions expire; re-run so the credential "+
			"helper fetches a fresh one (e.g. `ct auth token`). The provider cannot renew a "+
			"session it was handed", ErrSessionExpired, method, path, strings.TrimSpace(string(raw)))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &StatusError{Method: method, Path: path, Status: resp.StatusCode, Body: string(raw)}
	}
	return raw, nil
}

// doItem is do for a path that addresses ONE row. It refuses an empty id and is
// the only place a 404 becomes ErrNotFound.
func (c *Client) doItem(ctx context.Context, method, collection, id string, body any) ([]byte, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: %s %s", ErrNoID, method, collection)
	}
	raw, err := c.do(ctx, method, collection+"/"+id, body)
	var se *StatusError
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		return nil, ErrNotFound
	}
	return raw, err
}

// envelope is ChurchTools' universal `{"data": ...}` wrapper. List endpoints
// additionally carry `meta.pagination`, which MUST be followed: dropping it
// silently truncates a collection to its first page.
type envelope struct {
	Data json.RawMessage `json:"data"`
	Meta *struct {
		Pagination *struct {
			Current  *int `json:"current"`
			LastPage *int `json:"lastPage"`
		} `json:"pagination"`
	} `json:"meta"`
}

func unwrap(raw []byte, into any) error {
	_, err := unwrapEnvelope(raw, into)
	return err
}

// unwrapEnvelope decodes `data` into `into` and hands back the envelope so a
// caller can inspect `meta.pagination`.
func unwrapEnvelope(raw []byte, into any) (envelope, error) {
	var env envelope
	// A 204 has no body at all — CT answers PUT on /group/targetgroups and
	// /group/agegroups that way. No body is no data, not a decoding error.
	if len(bytes.TrimSpace(raw)) == 0 {
		return env, nil
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return env, fmt.Errorf("churchtools: decoding response envelope: %w", err)
	}
	if len(env.Data) == 0 {
		return env, nil
	}
	return env, json.Unmarshal(env.Data, into)
}

// morePages reports whether the envelope says further pages exist. No
// pagination block at all means the endpoint is not a paged list.
func (e envelope) morePages(page int) bool {
	if e.Meta == nil || e.Meta.Pagination == nil {
		return false
	}
	p := e.Meta.Pagination
	if p.Current == nil || p.LastPage == nil {
		return false
	}
	return *p.Current < *p.LastPage
}

// doWithCookie is do() plus the session cookie. The CSRF read needs the cookie
// from the whoami step, and that step is the only thing that can set it.
// doWithCookie is a GET-style read that carries the session cookie. It sends no
// body: the only call that needs one goes through AjaxJSON.
func (c *Client) doWithCookie(ctx context.Context, method, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/api"+path, nil)
	if err != nil {
		return nil, err
	}
	c.authenticate(req, method)
	req.Header.Set("Accept", "application/json")
	if !c.usesSession() && c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("churchtools: %s %s returned %d: %s", method, path, resp.StatusCode, string(raw))
	}
	return raw, nil
}

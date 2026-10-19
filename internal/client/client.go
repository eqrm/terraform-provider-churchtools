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

	// The session bought with the login token (see session.go), acquired lazily
	// on the first request and shared by REST and the legacy endpoint.
	//
	// One Client is shared by every resource (see provider.ProviderData) and
	// Terraform applies resources concurrently, so these fields are written
	// from several goroutines and need the mutex. The mutex is held across the
	// handshake, which is what makes it single-flight.
	sessionSem chan struct{} // 1-slot lock, see lockSession
	cookie     string
	csrfToken  string
	renewals   int
	// transientFails counts consecutive handshakes that failed for reasons that
	// say nothing about the token (5xx, network).
	transientFails int
	// handshakeErr remembers a failed handshake so the other ~340 resources do
	// not each retry it against a host that is already refusing logins.
	handshakeErr error

	// See LockMasterDataCreate.
	masterDataCreateMu sync.Mutex

	// See OfferedAuthIDs. Only a successful answer is cached, so a transient
	// error is retried by the next caller instead of being remembered.
	offeredMu sync.Mutex
	offered   map[int64]bool
}

// BaseURL is the host this client talks to, for messages.
func (c *Client) BaseURL() string { return c.baseURL }

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

		sessionSem: make(chan struct{}, 1),
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

// credentials returns the cookie and CSRF token every REST call carries.
//
// Both modes authenticate the same way on the wire -- the session cookie, plus
// the CSRF header on writes, exactly what a browser does. They differ only in
// where the session comes from: session mode was handed one by the operator;
// token mode buys one ONCE per Client (session.go) and reuses it.
//
// Token mode used to send `Authorization: Login <token>` on every request.
// ChurchTools treats each such request as a fresh login ("Erfolgreich
// angemeldet ... mittels logintoken" in its log), so a plan over ~340 objects
// meant ~340 logins and tripped the login rate limit (HTTP 429).
func (c *Client) credentials(ctx context.Context) (cookie, csrf string, err error) {
	return c.session(ctx)
}

// setAuth applies a session to a request: cookie always, CSRF on writes.
func setAuth(req *http.Request, method, cookie, csrf string) {
	req.Header.Set("Cookie", cookie)
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("CSRF-Token", csrf)
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
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("churchtools: encoding request body: %w", err)
		}
		payload = b
	}

	cookie, csrf, err := c.credentials(ctx)
	if err != nil {
		return nil, err
	}
	status, raw, err := c.roundTrip(ctx, method, path, payload, cookie, csrf)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized && !c.usesSession() {
		// The session we bought has lapsed. Renew it ONCE (shared: concurrent
		// requests that all saw the 401 trigger a single login) and retry this
		// request ONCE. A second 401 is not an expiry, so no further loop.
		cookie, csrf, err = c.renewSession(ctx, cookie)
		if err != nil {
			return nil, err
		}
		status, raw, err = c.roundTrip(ctx, method, path, payload, cookie, csrf)
		if err != nil {
			return nil, err
		}
		if status == http.StatusUnauthorized {
			return nil, fmt.Errorf("churchtools: %s %s was refused (401) even with a freshly "+
				"acquired session; the login token is probably not allowed to do this: %w",
				method, path, &StatusError{Method: method, Path: path, Status: status, Body: string(raw)})
		}
	}
	if status == http.StatusUnauthorized && c.usesSession() {
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
	if status < 200 || status > 299 {
		return nil, &StatusError{Method: method, Path: path, Status: status, Body: string(raw)}
	}
	return raw, nil
}

// roundTrip performs one authenticated REST request and returns the status and
// body without judging them.
func (c *Client) roundTrip(ctx context.Context, method, path string, payload []byte, cookie, csrf string) (int, []byte, error) {
	var buf io.Reader
	if payload != nil {
		buf = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/api"+path, buf)
	if err != nil {
		return 0, nil, err
	}
	setAuth(req, method, cookie, csrf)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, raw, nil
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

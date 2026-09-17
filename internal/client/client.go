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
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
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
	req.Header.Set("Authorization", "Login "+c.token)
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

// envelope is ChurchTools' universal `{"data": ...}` wrapper.
type envelope struct {
	Data json.RawMessage `json:"data"`
}

func unwrap(raw []byte, into any) error {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("churchtools: decoding response envelope: %w", err)
	}
	if len(env.Data) == 0 {
		return nil
	}
	return json.Unmarshal(env.Data, into)
}

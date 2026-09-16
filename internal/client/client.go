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

// ErrNotFound is returned when ChurchTools answers 404, so a resource can be
// removed from state rather than failing the whole plan.
var ErrNotFound = errors.New("churchtools: resource not found")

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
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("churchtools: %s %s returned %d: %s", method, path, resp.StatusCode, string(raw))
	}
	return raw, nil
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

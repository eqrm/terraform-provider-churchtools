package client

import (
	"context"
	"net/http"
)

func (c *Client) List(ctx context.Context, collection string) ([]Row, error) {
	raw, err := c.do(ctx, http.MethodGet, collection, nil)
	if err != nil {
		return nil, err
	}
	var rows []Row
	if err := unwrap(raw, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// Get fetches one row. `id` is a string because Terraform import ids are
// strings and because CT ids can legitimately be "0" (the Mainz campus).
func (c *Client) Get(ctx context.Context, collection, id string) (Row, error) {
	raw, err := c.doItem(ctx, http.MethodGet, collection, id, nil)
	if err != nil {
		return nil, err
	}
	var row Row
	if err := unwrap(raw, &row); err != nil {
		return nil, err
	}
	return row, nil
}

func (c *Client) Create(ctx context.Context, collection string, body Row) (Row, error) {
	raw, err := c.do(ctx, http.MethodPost, collection, body)
	if err != nil {
		return nil, err
	}
	var row Row
	if err := unwrap(raw, &row); err != nil {
		return nil, err
	}
	return row, nil
}

// Update uses the method the collection requires — CT is inconsistent: PUT for
// master data, PATCH for groups. The caller passes it in.
func (c *Client) Update(ctx context.Context, collection, id, method string, body Row) (Row, error) {
	raw, err := c.doItem(ctx, method, collection, id, body)
	if err != nil {
		return nil, err
	}
	var row Row
	if err := unwrap(raw, &row); err != nil {
		return nil, err
	}
	return row, nil
}

func (c *Client) Delete(ctx context.Context, collection, id string) error {
	_, err := c.doItem(ctx, http.MethodDelete, collection, id, nil)
	return err
}

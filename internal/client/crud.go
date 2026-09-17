package client

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// listPageSize is the `limit` sent with every page request, and maxListPages is
// a hard stop so a malformed pagination block cannot loop forever.
const (
	listPageSize = 100
	maxListPages = 500
)

// List reads a whole collection, following CT's `meta.pagination` across pages.
// Reading only page 1 would silently truncate the collection -- departments
// resolve their id by filtering this list, so a short read lets the
// duplicate-name guard pass and writes a second Bereich of the same name.
func (c *Client) List(ctx context.Context, collection string) ([]Row, error) {
	var all []Row
	for page := 1; page <= maxListPages; page++ {
		raw, err := c.do(ctx, http.MethodGet, pagedPath(collection, page, listPageSize), nil)
		if err != nil {
			return nil, err
		}
		var rows []Row
		env, err := unwrapEnvelope(raw, &rows)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
		if !env.morePages(page) {
			return all, nil
		}
	}
	return nil, fmt.Errorf("churchtools: %s reported more than %d pages; refusing to keep paging",
		collection, maxListPages)
}

func pagedPath(collection string, page, limit int) string {
	sep := "?"
	if strings.Contains(collection, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%spage=%d&limit=%d", collection, sep, page, limit)
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

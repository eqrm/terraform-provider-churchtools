package client

import (
	"context"
	"errors"
	"net/http"
)

// GroupRolesCollection is CT's group-type role definitions: one row per role of
// a group type, whose id is also the group_type_role permission domain.
const GroupRolesCollection = "/group/roles"

// PutGroupRole sends PUT /group/roles/{id}. It does not decode the reply: the
// OpenAPI (CT 3.137) answers 204 No Content (not verified live); either
// way only the read-back after the write proves what was stored, so callers GET
// afterwards.
func (c *Client) PutGroupRole(ctx context.Context, id string, body Row) error {
	_, err := c.doItem(ctx, http.MethodPut, GroupRolesCollection, id, body)
	return err
}

// IsStatus reports a StatusError with this HTTP status.
func IsStatus(err error, status int) (*StatusError, bool) {
	var se *StatusError
	if errors.As(err, &se) && se.Status == status {
		return se, true
	}
	return nil, false
}

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// GroupRolesCollection is CT's group-type role definitions: one row per role of
// a group type, whose id is also the group_type_role permission domain.
const GroupRolesCollection = "/group/roles"

// PutGroupRole sends PUT /group/roles/{id}. It does not decode the reply: the
// OpenAPI answers 200 with the row, but only the read-back after the write
// proves what was stored, so callers GET afterwards.
func (c *Client) PutGroupRole(ctx context.Context, id string, body Row) error {
	_, err := c.doItem(ctx, http.MethodPut, GroupRolesCollection, id, body)
	return err
}

// DeleteGroupRole really deletes a role definition (DELETE, dryRun=false sent
// explicitly). CT answers 204, or 409 with a deletion report while memberships
// (archived ones too), group settings or other rows still reference the role,
// and 400 when it is the last default role of its type. A role that is already
// gone is not an error.
func (c *Client) DeleteGroupRole(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("%w: DELETE %s", ErrNoID, GroupRolesCollection)
	}
	_, err := c.do(ctx, http.MethodDelete, GroupRolesCollection+"/"+id+"?dryRun=false", nil)
	var se *StatusError
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		return nil
	}
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

// DeletionBlockers extracts the messages of a 409 deletion report
// ({"data":{"deletionBlockers":[{"message":…}]}}), best effort.
func (e *StatusError) DeletionBlockers() []string {
	var rep struct {
		Data struct {
			Blockers []struct {
				Message string `json:"message"`
			} `json:"deletionBlockers"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(e.Body), &rep); err != nil {
		return nil
	}
	var out []string
	for _, b := range rep.Data.Blockers {
		if m := strings.TrimSpace(b.Message); m != "" {
			out = append(out, m)
		}
	}
	return out
}

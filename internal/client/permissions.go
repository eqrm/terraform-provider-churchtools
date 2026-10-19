package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// PermissionRow is one raw stored assignment from GET /permissions/{domainType}/{domainId}.
//
// Shape probed read-only on eqrm-dev on 2026-10-19 (group_role 14853 and 16829):
//
//	{"domainType":"group_role","domainId":16829,"authId":102,"dataId":2,
//	 "isInherited":false,"type":"grant","reason":null,
//	 "meta":{"modifiedDate":"…","modifiedPid":1}}
//
// CT returns one row per dataId; dataId is null for a right without a data
// selection. isInherited is part of the row's identity: a direct row applies in
// the group itself, an inherited row in its SUBGROUPS (provider issue #25).
type PermissionRow struct {
	AuthID      int64  `json:"authId"`
	DataID      *int64 `json:"dataId"`
	IsInherited bool   `json:"isInherited"`
	Type        string `json:"type"`
	Meta        *struct {
		ModifiedPid *int64 `json:"modifiedPid"`
	} `json:"meta"`
}

// IsSystemBaseline reports a row ChurchTools authored itself (modifiedPid -1).
// ct-cli never writes or revokes these, and neither does this provider.
func (r PermissionRow) IsSystemBaseline() bool {
	return r.Meta != nil && r.Meta.ModifiedPid != nil && *r.Meta.ModifiedPid == -1
}

// PermissionWrite is the body of PUT and DELETE on /permissions/{domainType}/{domainId}.
//
// The OpenAPI contract (CT 3.137, checked 2026-10-19) has two traps this type
// exists to make visible at the call site:
//
//   - PUT is ADDITIVE. dataId omitted/null/[] writes a null-data row; a
//     non-empty list adds those selections and leaves the others.
//   - DELETE with dataId omitted/null/[] removes EVERY selection row of that
//     authId and inheritance mode, not just the null row. Revoking one scoped
//     row must always send its exact id.
//
// DataID is therefore a slice that is omitted when empty, and callers must
// never build a DELETE for a scoped row without it (see planGrantWrites).
type PermissionWrite struct {
	AuthID      int64   `json:"authId"`
	DataID      []int64 `json:"dataId,omitempty"`
	IsInherited bool    `json:"isInherited"`
	Type        string  `json:"type,omitempty"`
}

func permissionPath(domainType, domainID string) (string, error) {
	if domainID == "" {
		return "", fmt.Errorf("%w: /permissions/%s", ErrNoID, domainType)
	}
	return "/permissions/" + domainType + "/" + domainID, nil
}

// PermissionRows reads every stored row of one permission domain. The endpoint
// is documented as un-paged ("No pagination … is applied"), so a single GET is
// the whole set. CT also documents that only authIds of the currently loaded
// module table come back: a grant for a module the host lacks is accepted by
// PUT and never read back (the dev 701/702/2080 case).
func (c *Client) PermissionRows(ctx context.Context, domainType, domainID string) ([]PermissionRow, error) {
	p, err := permissionPath(domainType, domainID)
	if err != nil {
		return nil, err
	}
	raw, err := c.do(ctx, http.MethodGet, p, nil)
	if err != nil {
		return nil, err
	}
	var rows []PermissionRow
	if _, err := unwrapEnvelope(raw, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// PutPermission adds rows (204, no body).
func (c *Client) PutPermission(ctx context.Context, domainType, domainID string, w PermissionWrite) error {
	p, err := permissionPath(domainType, domainID)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPut, p, w)
	return err
}

// DeletePermission removes rows (204, no body). See PermissionWrite for why an
// empty DataID is a wide delete.
func (c *Client) DeletePermission(ctx context.Context, domainType, domainID string, w PermissionWrite) error {
	p, err := permissionPath(domainType, domainID)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodDelete, p, w)
	return err
}

// GroupRoles reads GET /groups/{id}/roles: every role of the group's type,
// each with the (group, role) PAIRING id that is the group_role permission
// domain. CT creates the pairings with the group, so there is nothing to POST.
func (c *Client) GroupRoles(ctx context.Context, groupID string) ([]Row, error) {
	if groupID == "" {
		return nil, fmt.Errorf("%w: /groups/{id}/roles", ErrNoID)
	}
	raw, err := c.do(ctx, http.MethodGet, "/groups/"+groupID+"/roles", nil)
	if err != nil {
		return nil, err
	}
	var rows []Row
	env, err := unwrapEnvelope(raw, &rows)
	if err != nil {
		return nil, err
	}
	if env.morePages(1) {
		// Not seen live (a group type has a handful of roles). Refuse rather than
		// resolve a role name against a truncated list.
		return nil, fmt.Errorf("churchtools: /groups/%s/roles is paged; refusing to read a partial role list", groupID)
	}
	return rows, nil
}

// DecodePermissionRows decodes a bare JSON array of rows, as captured in testdata.
func DecodePermissionRows(raw []byte) ([]PermissionRow, error) {
	var rows []PermissionRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

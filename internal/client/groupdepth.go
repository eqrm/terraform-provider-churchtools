package client

import (
	"context"
	"fmt"
	"strconv"
)

// The per-group "subgroup permission depth" (Berechtigungen -> "Wie viele Ebenen
// von Untergruppen sollen einbezogen werden?"). It is NOT in GET /groups/{id}
// and not the group type's permissionDepth: it lives on the legacy churchauth
// ajax (measured on eqrm-dev, 2026-11-19).
//
//	read : func=getMasterData -> data.churchauth.group["<id>"].permission_deep_no,
//	       a STRING, for every group in one ~3.9 MB answer
//	write: func=updateDeepNo, domain_type=groupMemberstatus, domain_id=<id>,
//	       deep_no=<n> -> {"status":"success","data":null}
//
// CT validates neither side of the write: deep_no=7 was stored, and an unknown
// domain_id answered success. The caller validates the value (SubgroupDepths),
// and SetGroupPermissionDepth reads back, so a write CT ignored is an error.

// SubgroupDepths are the values the classic UI offers; 999 is "Alle Ebenen".
var SubgroupDepths = []int64{0, 1, 2, 3, 999}

// ValidSubgroupDepth reports whether n is one of the values the UI offers.
func ValidSubgroupDepth(n int64) bool {
	for _, v := range SubgroupDepths {
		if v == n {
			return true
		}
	}
	return false
}

// GroupPermissionDepth reads permission_deep_no of one group. The ~4 MB answer
// covers all groups, so it is cached per Client (one per provider instance) and
// dropped by every write. A group missing from the answer is an error, not 0:
// CT also reports success for writes to ids it does not know.
func (c *Client) GroupPermissionDepth(ctx context.Context, groupID string) (int64, error) {
	if c == nil {
		return 0, fmt.Errorf("%w: churchauth/ajax getMasterData", ErrNotConfigured)
	}
	c.depthMu.Lock()
	defer c.depthMu.Unlock()
	if c.depths == nil {
		var payload struct {
			Churchauth struct {
				Group map[string]struct {
					DeepNo any `json:"permission_deep_no"`
				} `json:"group"`
			} `json:"churchauth"`
		}
		if err := c.AjaxJSON(ctx, "churchauth", map[string]string{"func": "getMasterData"}, &payload); err != nil {
			return 0, fmt.Errorf("subgroup permission depth of %s unreadable: %w", c.baseURL, err)
		}
		if payload.Churchauth.Group == nil {
			return 0, fmt.Errorf("churchtools: churchauth/ajax getMasterData on %s carries no churchauth.group", c.baseURL)
		}
		depths := make(map[string]string, len(payload.Churchauth.Group))
		for id, g := range payload.Churchauth.Group {
			// A string on live CT ("1"); accept a bare number too.
			depths[id] = fmt.Sprint(g.DeepNo)
		}
		c.depths = depths
	}
	raw, ok := c.depths[groupID]
	if !ok {
		return 0, fmt.Errorf("churchtools: group %s is not in churchauth getMasterData on %s (no such group, or no right to see its permissions)", groupID, c.baseURL)
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("churchtools: permission_deep_no of group %s is %q, not a number", groupID, raw)
	}
	return n, nil
}

// SetGroupPermissionDepth writes the depth and drops the cached read. CT answers
// success for any value and any id, so the caller must read back.
func (c *Client) SetGroupPermissionDepth(ctx context.Context, groupID string, depth int64) error {
	if c == nil {
		return fmt.Errorf("%w: churchauth/ajax updateDeepNo", ErrNotConfigured)
	}
	if groupID == "" {
		return fmt.Errorf("%w: churchauth/ajax updateDeepNo", ErrNoID)
	}
	if !ValidSubgroupDepth(depth) {
		return fmt.Errorf("churchtools: subgroup permission depth %d is not one of %v", depth, SubgroupDepths)
	}
	c.depthMu.Lock()
	c.depths = nil // dropped before and after: a failed write may still have landed
	c.depthMu.Unlock()
	err := c.Ajax(ctx, "churchauth", map[string]string{
		"func": "updateDeepNo", "domain_type": "groupMemberstatus",
		"domain_id": groupID, "deep_no": strconv.FormatInt(depth, 10),
	})
	c.depthMu.Lock()
	c.depths = nil
	c.depthMu.Unlock()
	return err
}

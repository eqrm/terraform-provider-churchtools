package testmock

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// The permission routes: GET /groups/{id}/roles and GET/PUT/DELETE
// /permissions/group_role/{pairingId}. They sit beside the generic row store
// because their semantics are not CRUD on a row, and the provider's safety
// depends on exactly those semantics (CT 3.137 OpenAPI, checked 2026-10-19):
//
//   - PUT is additive; dataId omitted/null/[] writes the null-data row;
//   - a non-empty PUT is silently ignored while a -1 row exists for the key;
//   - DELETE with dataId omitted/null/[] removes EVERY row of (authId,
//     isInherited), whatever its dataId or type; a list deletes exact rows;
//   - a right of a module the host lacks is answered 204 and never stored
//     (eqrm-dev: 701/702/2080);
//   - every write answers 204 with no body; a missing row deletes fine.

// PermRow is one stored row, in CT's wire shape.
type PermRow struct {
	AuthID      int64  `json:"authId"`
	DataID      *int64 `json:"dataId"`
	IsInherited bool   `json:"isInherited"`
	Type        string `json:"type"`
	ModifiedPid int64  `json:"-"`
}

func (r PermRow) wire(domainID string) map[string]any {
	var data any
	if r.DataID != nil {
		data = *r.DataID
	}
	id, _ := strconv.Atoi(domainID)
	return map[string]any{
		"domainType": "group_role", "domainId": id, "authId": r.AuthID, "dataId": data,
		"isInherited": r.IsInherited, "type": r.Type, "reason": nil,
		"meta": map[string]any{"modifiedDate": "2026-10-19T10:00:00Z", "modifiedPid": r.ModifiedPid},
	}
}

func (r PermRow) same(o PermRow) bool {
	return r.AuthID == o.AuthID && r.IsInherited == o.IsInherited && r.Type == o.Type &&
		((r.DataID == nil && o.DataID == nil) || (r.DataID != nil && o.DataID != nil && *r.DataID == *o.DataID))
}

type permState struct {
	groupRoles     map[string][]map[string]any // group id -> /groups/{id}/roles rows
	rows           map[string][]PermRow        // pairing id -> rows
	inactive       map[int64]bool
	dropped        map[int64]bool
	authTableFails bool
	authTableCalls int
	writes         []string
}

func (s *Server) perms() *permState {
	if s.perm == nil {
		s.perm = &permState{groupRoles: map[string][]map[string]any{}, rows: map[string][]PermRow{}, inactive: map[int64]bool{}}
	}
	return s.perm
}

// SeedGroupRoles installs a group's role list: name -> pairing id.
func (s *Server) SeedGroupRoles(groupID int, roles map[string]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []map[string]any
	for name, id := range roles {
		rows = append(rows, map[string]any{"id": id, "name": name, "nameTranslated": name, "groupTypeRoleId": id % 100})
		if _, ok := s.perms().rows[strconv.Itoa(id)]; !ok {
			s.perms().rows[strconv.Itoa(id)] = nil
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i]["id"].(int) < rows[j]["id"].(int) })
	s.perms().groupRoles[strconv.Itoa(groupID)] = rows
}

// SeedPermissions replaces a pairing's stored rows, e.g. from a captured fixture
// or to simulate a click in the CT UI between two plans.
func (s *Server) SeedPermissions(pairingID int, rows []PermRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.perms().rows[strconv.Itoa(pairingID)] = append([]PermRow(nil), rows...)
}

// AddPermission adds one row as an admin would in the UI.
func (s *Server) AddPermission(pairingID int, row PermRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := strconv.Itoa(pairingID)
	s.perms().rows[k] = append(s.perms().rows[k], row)
}

// Permissions returns a pairing's stored rows.
func (s *Server) Permissions(pairingID int) []PermRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]PermRow(nil), s.perms().rows[strconv.Itoa(pairingID)]...)
}

// PermissionWrites lists every PUT/DELETE as "METHOD authId inherited [ids]",
// so a test can assert exactly what went over the wire.
func (s *Server) PermissionWrites() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.perms().writes...)
}

// SetInactiveAuthIDs makes PUTs for these authIds answer 204 without storing,
// like a host that lacks the right's module.
func (s *Server) SetInactiveAuthIDs(ids ...int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		s.perms().inactive[id] = true
	}
}

// SetDroppedAuthIDs makes PUTs for these authIds answer 204 without storing
// although the host still lists the right in its table.
func (s *Server) SetDroppedAuthIDs(ids ...int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perms().dropped == nil {
		s.perms().dropped = map[int64]bool{}
	}
	for _, id := range ids {
		s.perms().dropped[id] = true
	}
}

// FailAuthTable makes getMasterData answer an error envelope, so the provider
// cannot learn which rights the host offers.
func (s *Server) FailAuthTable() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.perms().authTableFails = true
}

// AuthTableCalls counts how often the host's right table was asked for.
func (s *Server) AuthTableCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.perms().authTableCalls
}

// authTable is data.auth_table of churchauth/ajax getMasterData: {module:
// {right: {id}}}. A host lacking a module does not list its rights, which is
// what SetInactiveAuthIDs models on the PUT side. The mock has no catalog, so
// it offers every id of the ranges the real table uses (1..2100, 10000..10999)
// except the inactive ones.
func (ps *permState) authTable() map[string]any {
	rights := map[string]any{}
	add := func(id int64) {
		if !ps.inactive[id] {
			rights["right"+strconv.FormatInt(id, 10)] = map[string]any{"id": id}
		}
	}
	for id := int64(1); id <= 2100; id++ {
		add(id)
	}
	for id := int64(10000); id <= 10999; id++ {
		add(id)
	}
	return map[string]any{"mock": rights}
}

// handlePermissions serves the permission routes and reports whether it did.
// Caller holds s.mu.
func (s *Server) handlePermissions(w http.ResponseWriter, r *http.Request) bool {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api"), "/"), "/")
	ps := s.perms()
	switch {
	case len(parts) == 3 && parts[0] == "groups" && parts[2] == "roles":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			writeData(w, map[string]any{"message": "no " + r.Method + " on /groups/{id}/roles"})
			return true
		}
		rows, ok := ps.groupRoles[parts[1]]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			writeData(w, map[string]any{"message": "group not found"})
			return true
		}
		writeData(w, rows)
		return true
	case len(parts) == 3 && parts[0] == "permissions" && parts[1] == "group_role":
	default:
		return false
	}

	id := parts[2]
	stored, ok := ps.rows[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		writeData(w, map[string]any{"message": "domain not found"})
		return true
	}
	switch r.Method {
	case http.MethodGet:
		out := make([]map[string]any, 0, len(stored))
		for _, row := range stored {
			if ps.inactive[row.AuthID] {
				continue
			}
			out = append(out, row.wire(id))
		}
		writeData(w, out)
		return true
	case http.MethodPut, http.MethodDelete:
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return true
	}

	var body struct {
		AuthID      *int64  `json:"authId"`
		DataID      []int64 `json:"dataId"`
		IsInherited bool    `json:"isInherited"`
		Type        string  `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AuthID == nil {
		w.WriteHeader(http.StatusBadRequest)
		writeData(w, map[string]any{"message": "authId is required"})
		return true
	}
	ps.writes = append(ps.writes, writeLog(r.Method, *body.AuthID, body.IsInherited, body.DataID))
	auth := *body.AuthID

	if r.Method == http.MethodDelete {
		kept := stored[:0:0]
		for _, row := range stored {
			hit := row.AuthID == auth && row.IsInherited == body.IsInherited
			if hit && len(body.DataID) > 0 {
				hit = row.DataID != nil && containsID(body.DataID, *row.DataID)
			}
			if !hit {
				kept = append(kept, row)
			}
		}
		ps.rows[id] = kept
		w.WriteHeader(http.StatusNoContent)
		return true
	}

	typ := body.Type
	if typ == "" {
		typ = "grant"
	}
	if ps.dropped[auth] {
		// a host that OFFERS the right yet does not store this write (a bug, a
		// permission quirk): the case the read-back guard exists for.
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	if ps.inactive[auth] {
		// accepted, stored where no GET will ever show it: like CT.
		ps.rows[id] = append(stored, PermRow{AuthID: auth, IsInherited: body.IsInherited, Type: typ, ModifiedPid: 1})
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	if len(body.DataID) == 0 {
		add := PermRow{AuthID: auth, IsInherited: body.IsInherited, Type: typ, ModifiedPid: 1}
		if !hasRow(stored, add) {
			stored = append(stored, add)
		}
	} else {
		all := int64(-1)
		if !hasRow(stored, PermRow{AuthID: auth, DataID: &all, IsInherited: body.IsInherited, Type: "grant"}) {
			for _, d := range body.DataID {
				d := d
				add := PermRow{AuthID: auth, DataID: &d, IsInherited: body.IsInherited, Type: typ, ModifiedPid: 1}
				if !hasRow(stored, add) {
					stored = append(stored, add)
				}
			}
		}
	}
	ps.rows[id] = stored
	w.WriteHeader(http.StatusNoContent)
	return true
}

func hasRow(rows []PermRow, r PermRow) bool {
	for _, x := range rows {
		if x.same(r) {
			return true
		}
	}
	return false
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func writeLog(method string, auth int64, inherited bool, ids []int64) string {
	s := method + " " + strconv.FormatInt(auth, 10)
	if inherited {
		s += " inherited"
	}
	if len(ids) > 0 {
		strs := make([]string, len(ids))
		for i, id := range ids {
			strs[i] = strconv.FormatInt(id, 10)
		}
		s += " [" + strings.Join(strs, ",") + "]"
	}
	return s
}

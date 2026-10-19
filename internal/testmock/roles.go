package testmock

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Group-type role definitions, /group/roles (CT 3.137 OpenAPI, checked
// 2026-10-19). The generic row store cannot model them because the contract is
// stricter than CRUD on a map:
//
//   - POST and PUT both require all eight writable fields, and PUT is not
//     partial ("Send all required fields");
//   - nameTranslated, isLeader and id are read-only: a body that carries them is
//     accepted and they are ignored. nameTranslated is rendered per request
//     language, modelled here as the stored name;
//   - DELETE answers 204, or 409 with a deletion report while members (or any
//     other reference) still point at the role. dryRun=true answers the report
//     as a 409 even when deleting would work.

var roleRequired = []string{"groupTypeId", "name", "shorty", "sortKey", "type", "growPathId", "isDefault", "isHidden"}

// SetRoleMembers makes DELETE on this role answer 409, like a role that
// memberships (also archived ones) still reference.
func (s *Server) SetRoleMembers(id int, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.roleMembers == nil {
		s.roleMembers = map[string]int{}
	}
	s.roleMembers[strconv.Itoa(id)] = n
}

// RoleDeletes counts the DELETE calls that reached a role (dry runs excluded).
func (s *Server) RoleDeletes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.roleDeletes
}

func roleStored(body map[string]any) map[string]any {
	row := map[string]any{}
	for _, k := range roleRequired {
		row[k] = body[k]
	}
	row["nameTranslated"] = body["name"]
	row["isLeader"] = body["type"] == "leader"
	return row
}

// handleRoleWrite serves POST, PUT and DELETE on /group/roles; GET falls
// through to the generic store. Caller holds s.mu.
func (s *Server) handleRoleWrite(w http.ResponseWriter, r *http.Request, id string) bool {
	if s.rows["/group/roles"] == nil {
		s.rows["/group/roles"] = map[string]any{}
	}
	rows := s.rows["/group/roles"]
	switch r.Method {
	case http.MethodPost:
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if missing := missingKeys(body, roleRequired); len(missing) > 0 {
			w.WriteHeader(http.StatusBadRequest)
			writeData(w, map[string]any{"message": "There are validation errors", "missing": missing})
			return true
		}
		s.nextID++
		row := roleStored(body)
		row["id"] = float64(s.nextID)
		rows[strconv.Itoa(s.nextID)] = row
		s.keepOneDefaultRole(strconv.Itoa(s.nextID), row)
		w.WriteHeader(http.StatusCreated)
		writeData(w, row)
		return true
	case http.MethodPut:
		cur, ok := rows[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		s.lastPut["/group/roles"] = body
		if missing := missingKeys(body, roleRequired); len(missing) > 0 {
			w.WriteHeader(http.StatusBadRequest)
			writeData(w, map[string]any{"message": "There are validation errors", "missing": missing})
			return true
		}
		row := roleStored(body)
		row["id"] = cur.(map[string]any)["id"]
		rows[id] = row
		s.keepOneDefaultRole(id, row)
		writeData(w, row)
		return true
	case http.MethodDelete:
		if _, ok := rows[id]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		dry := r.URL.Query().Get("dryRun") == "true"
		n := s.roleMembers[id]
		if dry || n > 0 {
			w.WriteHeader(http.StatusConflict)
			blockers := []any{}
			if n > 0 {
				blockers = append(blockers, map[string]any{"message": "Die Rolle wird noch von " + strconv.Itoa(n) + " Mitgliedschaft(en) verwendet."})
			}
			writeData(w, map[string]any{
				"deletable": n == 0, "deletionBlockers": blockers, "message": nil,
				"references": []any{map[string]any{"blocksDeletion": n > 0, "columnName": "group_type_role_id", "count": n, "tableName": "cdb_gruppenteilnehmer", "type": "ENTRY"}},
			})
			return true
		}
		s.roleDeletes++
		delete(rows, id)
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}

// keepOneDefaultRole: isDefault=true clears the flag on the other roles of the
// type. Caller holds s.mu.
func (s *Server) keepOneDefaultRole(id string, row map[string]any) {
	if row["isDefault"] != true {
		return
	}
	for other, v := range s.rows["/group/roles"] {
		o := v.(map[string]any)
		if other != id && o["groupTypeId"] == row["groupTypeId"] {
			o["isDefault"] = false
		}
	}
}

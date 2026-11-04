package testmock

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
)

// Growth paths (Wachstumspfade), /person/growpaths. Measured on eqrm-dev and
// checked against the CT OpenAPI spec:
//
//   - POST needs name (1-100 chars) and color (enum below); sortKey defaults to
//     0. Answers 201 with the row.
//   - PUT needs name and color. A body WITHOUT sortKey resets it to 0. It does
//     not recalculate the growPathId already stored on persons or roles.
//     The PUT reply status is not measured; 204 is assumed (the provider never
//     parses it).
//   - DELETE answers 204, or 409 while a group-type role or group role still
//     references the stage; dryRun=true reports the dependencies as a 409
//     without deleting. A DELETE of an unknown id answers 204 and does nothing.
//   - GET of an unknown id is 404 (served by the generic store).

const growPathCollection = "/person/growpaths"

var growPathColors = map[string]bool{
	"accent": true, "amber": true, "basic": true, "blue": true, "constructive": true,
	"critical": true, "cyan": true, "destructive": true, "emerald": true, "error": true,
	"fuchsia": true, "gray": true, "green": true, "indigo": true, "info": true,
	"lime": true, "magic": true, "orange": true, "pink": true, "purple": true,
	"red": true, "rose": true, "sky": true, "success": true, "teal": true,
	"violet": true, "warning": true, "yellow": true,
}

// GrowPathDeletes counts the DELETE calls that actually removed a stage.
func (s *Server) GrowPathDeletes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.growPathDeletes
}

func growPathInvalid(body map[string]any) string {
	name, _ := body["name"].(string)
	if n := len([]rune(name)); n < 1 || n > 100 {
		return "name: must be 1-100 characters"
	}
	color, _ := body["color"].(string)
	if !growPathColors[color] {
		return "color: invalid value"
	}
	if v, ok := body["sortKey"]; ok {
		if f, isNum := v.(float64); !isNum || f < 0 || f != math.Trunc(f) {
			return "sortKey: must be an integer >= 0"
		}
	}
	return ""
}

// growPathRefs counts the roles that point at the stage (group roles and, via
// the same store, group-type roles). Caller holds s.mu.
func (s *Server) growPathRefs(id string) int {
	n := 0
	for _, v := range s.rows["/group/roles"] {
		if g, ok := v.(map[string]any)["growPathId"].(float64); ok && strconv.Itoa(int(g)) == id {
			n++
		}
	}
	return n
}

// handleGrowPathWrite serves POST, PUT and DELETE; GET falls through to the
// generic store. Caller holds s.mu.
func (s *Server) handleGrowPathWrite(w http.ResponseWriter, r *http.Request, id string) bool {
	rows := s.rows[growPathCollection]
	switch r.Method {
	case http.MethodPost:
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		s.lastPost[growPathCollection] = body
		if msg := growPathInvalid(body); msg != "" {
			w.WriteHeader(http.StatusBadRequest)
			writeData(w, map[string]any{"message": "There are validation errors", "detail": msg})
			return true
		}
		sortKey := float64(0)
		if v, ok := body["sortKey"].(float64); ok {
			sortKey = v
		}
		s.nextID++
		row := map[string]any{
			"id": float64(s.nextID), "name": body["name"], "color": body["color"], "sortKey": sortKey,
		}
		rows[strconv.Itoa(s.nextID)] = row
		w.WriteHeader(http.StatusCreated)
		writeData(w, row)
		return true
	case http.MethodPut:
		cur, ok := rows[id].(map[string]any)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		s.lastPut[growPathCollection] = body
		if msg := growPathInvalid(body); msg != "" {
			w.WriteHeader(http.StatusBadRequest)
			writeData(w, map[string]any{"message": "There are validation errors", "detail": msg})
			return true
		}
		sortKey := float64(0) // an omitted sortKey resets to 0
		if v, ok := body["sortKey"].(float64); ok {
			sortKey = v
		}
		cur["name"], cur["color"], cur["sortKey"] = body["name"], body["color"], sortKey
		if s.growPathPutHook != nil {
			s.growPathPutHook(cur)
		}
		w.WriteHeader(http.StatusNoContent)
		return true
	case http.MethodDelete:
		_, exists := rows[id]
		if !exists && r.URL.Query().Get("dryRun") != "true" {
			w.WriteHeader(http.StatusNoContent) // unknown id: 204, nothing happens
			return true
		}
		n := s.growPathRefs(id)
		if r.URL.Query().Get("dryRun") == "true" || n > 0 {
			w.WriteHeader(http.StatusConflict)
			writeData(w, map[string]any{
				"deletable":  n == 0,
				"references": []any{map[string]any{"blocksDeletion": n > 0, "count": n, "tableName": "cdb_gruppentyp_role"}},
			})
			return true
		}
		s.growPathDeletes++
		delete(rows, id)
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}

// SetGrowPathPutHook runs fn on the stored row after every successful PUT, so a
// test can make CT store something other than what was sent.
func (s *Server) SetGrowPathPutHook(fn func(row map[string]any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.growPathPutHook = fn
}

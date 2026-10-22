package testmock

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Dynamic groups: /dynamicgroups/{id}/ruleset and /dynamicgroups/{id}/status.
//
// What is modelled, from ct-cli (src/engine/synthetic.ts, dynamic.ts) and its
// live measurements on CT 3.134-3.137:
//   - GET ruleset answers a single-element array [RuleSet]; a group without a
//     ruleset answers 404.
//   - PUT ruleset wants {"dynamicGroupRuleSet":[RuleSet]}. A bare object or a bare
//     array is refused, as live CT does (ct-cli #77: "Array expected" / "Object
//     expected").
//   - GET/PUT status carry {"dynamicGroupStatus": "active"|"manual"|"none"}.
//   - The endpoints take any existing group id, archived (status 3) included.
//
// Not verified live by this provider (no write ran): the reply status and body of
// the two PUTs. The provider ignores both, and the mock answers 204 without a body.
// There is no /refresh route on purpose: the provider must never call it, and an
// unknown route answers 404.

type dynState struct {
	ruleset  any // exactly what was seeded or PUT, decoded; nil = no ruleset
	status   string
	putCount int // PUT /ruleset calls
	stCount  int // PUT /status calls
	lastPut  map[string]any
}

// SeedDynamic gives a group a ruleset exactly as live GET would return it: raw is a
// JSON document (typically with cosmetic dterm labels, string ints and
// timestamps) stored verbatim and served back inside a single-element array when
// it is an object. The group row itself must be seeded separately
// (Seed("/groups", ...)) or be created by the test.
func (s *Server) SeedDynamic(groupID int, status, rawRuleset string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v any
	if err := json.Unmarshal([]byte(rawRuleset), &v); err != nil {
		panic("testmock.SeedDynamic: " + err.Error())
	}
	st := s.dynFor(strconv.Itoa(groupID))
	st.ruleset, st.status = v, status
}

// Dynamic returns the stored ruleset (as decoded JSON, nil if none) and status.
func (s *Server) Dynamic(groupID int) (ruleset any, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.dynFor(strconv.Itoa(groupID))
	return st.ruleset, st.status
}

// DynamicPuts returns how often PUT ruleset and PUT status were called for a group.
func (s *Server) DynamicPuts(groupID int) (ruleset, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.dynFor(strconv.Itoa(groupID))
	return st.putCount, st.stCount
}

// LastRulesetPut returns the body of the most recent PUT ruleset for a group.
func (s *Server) LastRulesetPut(groupID int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dynFor(strconv.Itoa(groupID)).lastPut
}

func (s *Server) dynFor(id string) *dynState {
	if s.dyn == nil {
		s.dyn = map[string]*dynState{}
	}
	st, ok := s.dyn[id]
	if !ok {
		st = &dynState{}
		s.dyn[id] = st
	}
	return st
}

func (s *Server) handleDynamic(w http.ResponseWriter, r *http.Request) bool {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api"), "/"), "/")
	if len(parts) < 1 || parts[0] != "dynamicgroups" {
		return false
	}
	if len(parts) != 3 || (parts[2] != "ruleset" && parts[2] != "status") {
		w.WriteHeader(http.StatusNotFound)
		writeData(w, map[string]any{"message": "no route " + r.URL.Path})
		return true
	}
	id := parts[1]
	_, groupExists := s.rows["/groups"][id]
	st := s.dynFor(id)
	if !groupExists && st.ruleset == nil && st.status == "" {
		w.WriteHeader(http.StatusNotFound)
		return true
	}
	switch {
	case parts[2] == "ruleset" && r.Method == http.MethodGet:
		if st.ruleset == nil {
			w.WriteHeader(http.StatusNotFound)
			writeData(w, map[string]any{"message": "no ruleset"})
			return true
		}
		if _, isArr := st.ruleset.([]any); isArr {
			writeData(w, st.ruleset)
		} else {
			writeData(w, []any{st.ruleset})
		}
	case parts[2] == "ruleset" && r.Method == http.MethodPut:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeData(w, map[string]any{"message": "Object expected"})
			return true
		}
		arr, ok := body["dynamicGroupRuleSet"].([]any)
		if !ok || len(arr) != 1 || len(body) != 1 {
			w.WriteHeader(http.StatusInternalServerError)
			writeData(w, map[string]any{"message": "TypeException: Array expected at #->properties:dynamicGroupRuleSet"})
			return true
		}
		if _, isObj := arr[0].(map[string]any); !isObj {
			w.WriteHeader(http.StatusBadRequest)
			writeData(w, map[string]any{"message": "dynamicGroupRuleSet[0] must be an object"})
			return true
		}
		st.ruleset, st.lastPut = arr[0], body
		st.putCount++
		w.WriteHeader(http.StatusNoContent)
	case parts[2] == "status" && r.Method == http.MethodGet:
		if st.ruleset == nil {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		writeData(w, map[string]any{"dynamicGroupStatus": st.status})
	case parts[2] == "status" && r.Method == http.MethodPut:
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		v, _ := body["dynamicGroupStatus"].(string)
		if v != "active" && v != "manual" && v != "none" {
			w.WriteHeader(http.StatusBadRequest)
			writeData(w, map[string]any{"message": "dynamicGroupStatus: validation.always.invalid"})
			return true
		}
		st.status = v
		st.stCount++
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		writeData(w, map[string]any{"message": "ChurchTools serves no " + r.Method + " on " + r.URL.Path})
	}
	return true
}

package testmock

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Group type migration. Live CT refuses groupTypeId on PATCH /groups/{id}
// (`400 groupTypeId: validation.always.invalid`) and moves a group's type through
// POST /groups/{id}/grouptype instead (204, empty body), verified by ct-cli on
// CT 3.137.0-RC21. Roles come from the GET-only /group/roles collection, seeded with
// Seed("/group/roles", id, {"name", "groupTypeId", "type", "isDefault"}).

// SeedMembers sets a group's memberships as the groupTypeRoleId each person holds.
func (s *Server) SeedMembers(groupID int, roleIDs ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.members == nil {
		s.members = map[string][]int{}
	}
	s.members[strconv.Itoa(groupID)] = roleIDs
}

// Members returns the groupTypeRoleId of every membership of a group, sorted.
func (s *Server) Members(groupID int) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]int(nil), s.members[strconv.Itoa(groupID)]...)
	sort.Ints(out)
	return out
}

// LastGroupTypePost returns the body of the most recent POST /groups/{id}/grouptype
// for that group, or nil. Numbers are float64, as decoded JSON.
func (s *Server) LastGroupTypePost(groupID int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTypePost[strconv.Itoa(groupID)]
}

// handleGroupType serves /groups/{id}/members and /groups/{id}/grouptype.
func (s *Server) handleGroupType(w http.ResponseWriter, r *http.Request) bool {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api"), "/"), "/")
	if len(parts) != 3 || parts[0] != "groups" || (parts[2] != "members" && parts[2] != "grouptype") {
		return false
	}
	id := parts[1]
	row, ok := s.rows["/groups"][id].(map[string]any)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return true
	}
	wantMethod := http.MethodGet
	if parts[2] == "grouptype" {
		wantMethod = http.MethodPost
	}
	if r.Method != wantMethod {
		w.WriteHeader(http.StatusMethodNotAllowed)
		writeData(w, map[string]any{"message": "ChurchTools serves no " + r.Method + " on /groups/{id}/" + parts[2]})
		return true
	}

	if parts[2] == "members" {
		roleIDs := s.members[id]
		page, limit := pageParams(r)
		lo, hi := (page-1)*limit, page*limit
		if lo > len(roleIDs) {
			lo = len(roleIDs)
		}
		if hi > len(roleIDs) {
			hi = len(roleIDs)
		}
		list := []any{}
		for i, rid := range roleIDs[lo:hi] {
			list = append(list, map[string]any{"personId": float64(lo + i + 1), "groupTypeRoleId": float64(rid)})
		}
		lastPage := (len(roleIDs) + limit - 1) / limit
		if lastPage == 0 {
			lastPage = 1
		}
		writeDataMeta(w, list, map[string]any{"pagination": map[string]any{"current": page, "lastPage": lastPage}})
		return true
	}

	var body struct {
		GroupTypeID *float64           `json:"groupTypeId"`
		RoleMapping map[string]float64 `json:"roleMapping"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	bad := func(msg string) bool {
		w.WriteHeader(http.StatusBadRequest)
		writeData(w, map[string]any{"message": msg})
		return true
	}
	if body.GroupTypeID == nil {
		return bad("groupTypeId: validation.required")
	}
	info, _ := row["information"].(map[string]any)
	cur, _ := info["groupTypeId"].(float64)
	// Every role of the CURRENT type needs a target, and a target must belong to the new type.
	roleType := map[int]float64{}
	for k, v := range s.rows["/group/roles"] {
		n, _ := strconv.Atoi(k)
		roleType[n], _ = v.(map[string]any)["groupTypeId"].(float64)
	}
	for rid, t := range roleType {
		if t != cur {
			continue
		}
		to, ok := body.RoleMapping[strconv.Itoa(rid)]
		if !ok {
			return bad("roleMapping: role " + strconv.Itoa(rid) + " of the current group type is not mapped")
		}
		if roleType[int(to)] != *body.GroupTypeID {
			return bad("roleMapping: role " + strconv.Itoa(int(to)) + " is not a role of the new group type")
		}
	}
	if s.lastTypePost == nil {
		s.lastTypePost = map[string]map[string]any{}
	}
	raw := map[string]any{"groupTypeId": *body.GroupTypeID}
	mapping := map[string]any{}
	for k, v := range body.RoleMapping {
		mapping[k] = v
	}
	raw["roleMapping"] = mapping
	s.lastTypePost[id] = raw

	info["groupTypeId"] = *body.GroupTypeID
	for i, rid := range s.members[id] {
		if to, ok := body.RoleMapping[strconv.Itoa(rid)]; ok {
			s.members[id][i] = int(to)
		}
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}

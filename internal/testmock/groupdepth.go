package testmock

import "strconv"

// The per-group "subgroup permission depth" (Berechtigungen -> "Wie viele
// Ebenen von Untergruppen sollen einbezogen werden?"). It is not in REST. It
// lives on the legacy churchauth ajax (measured on eqrm-dev, 2026-11-19):
//
//	func=getMasterData -> data.churchauth.group["<id>"].permission_deep_no (a STRING)
//	func=updateDeepNo  domain_type=groupMemberstatus domain_id=<id> deep_no=<n>
//	                   -> {"status":"success","data":null}
//
// CT validates neither the value (deep_no=7 is stored) nor the group
// (domain_id=999999 answers success and stores nothing readable), and the mock
// mirrors that on purpose: a provider that trusts the reply would pass here.

// defaultGroupDepth is what a group without an explicit value reads as.
const defaultGroupDepth = "0"

type depthState struct {
	values map[string]string // group id -> permission_deep_no
	// ignore makes updateDeepNo answer success without storing anything.
	ignore bool
	reads  int // churchauth getMasterData calls
	writes []map[string]string
}

func (s *Server) depth() *depthState {
	if s.depthSt == nil {
		s.depthSt = &depthState{values: map[string]string{}}
	}
	return s.depthSt
}

// SetGroupDepth stores permission_deep_no for a group, as set in the UI.
func (s *Server) SetGroupDepth(groupID int, v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.depth().values[strconv.Itoa(groupID)] = v
}

// GroupDepth is what CT holds for a group (the default when never set).
func (s *Server) GroupDepth(groupID int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.depth().values[strconv.Itoa(groupID)]; ok {
		return v
	}
	return defaultGroupDepth
}

// IgnoreDeepNoWrites makes updateDeepNo succeed without storing the value.
func (s *Server) IgnoreDeepNoWrites() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.depth().ignore = true
}

// DepthReads counts churchauth getMasterData calls (the ~4 MB answer on a real host).
func (s *Server) DepthReads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.depth().reads
}

// DepthWrites counts updateDeepNo calls.
func (s *Server) DepthWrites() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.depth().writes)
}

// LastDepthWrite is the form of the latest updateDeepNo call.
func (s *Server) LastDepthWrite() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.depth().writes
	if len(w) == 0 {
		return nil
	}
	return w[len(w)-1]
}

// churchauthGroups is data.churchauth.group: one entry per existing group.
// Caller holds s.mu.
func (s *Server) churchauthGroups() map[string]any {
	s.depth().reads++
	out := map[string]any{}
	for id := range s.rows["/groups"] {
		v, ok := s.depth().values[id]
		if !ok {
			v = defaultGroupDepth
		}
		out[id] = map[string]any{"permission_deep_no": v}
	}
	return out
}

// updateDeepNo mirrors CT: any value and any domain_id answer success.
// Caller holds s.mu.
func (s *Server) updateDeepNo(form map[string]string) {
	d := s.depth()
	d.writes = append(d.writes, form)
	if d.ignore || form["domain_type"] != "groupMemberstatus" {
		return
	}
	if _, exists := s.rows["/groups"][form["domain_id"]]; !exists {
		return // CT answers success for a nonexistent id and stores nothing readable
	}
	d.values[form["domain_id"]] = form["deep_no"]
}

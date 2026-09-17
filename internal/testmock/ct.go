// Package testmock is an in-process ChurchTools stand-in. It holds rows in a
// map so acceptance tests exercise real CRUD round-trips with no network and
// no live instance — PR CI must never touch eqrm or eqrm-dev.
package testmock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

type Server struct {
	*httptest.Server
	mu     sync.Mutex
	rows   map[string]map[string]any // collection -> id -> row
	nextID int
}

func New() *Server {
	s := &Server{rows: map[string]map[string]any{}, nextID: 1000}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// Seed installs a row at a known id, which is how import tests get something
// to import. Pass id 0 to cover the Mainz-campus case.
func (s *Server) Seed(collection string, id int, row map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows[collection] == nil {
		s.rows[collection] = map[string]any{}
	}
	row["id"] = float64(id)
	s.rows[collection][strconv.Itoa(id)] = row
}

// Find returns the first row in a collection whose `field` equals `value`, so
// a test can assert on what the provider actually sent.
func (s *Server) Find(collection, field string, value any) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.rows[collection] {
		row := v.(map[string]any)
		if row[field] == value {
			return row
		}
	}
	return nil
}

// splitPath maps a request path onto (collection, id). CT nests some
// collections two deep (/group/grouptypes, /person/commentviewers), so the
// first segment alone is not enough to identify one.
func splitPath(path string) (collection, id string) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(path, "/api"), "/"), "/")
	switch parts[0] {
	case "group", "person":
		collection = "/" + parts[0] + "/" + parts[1]
		parts = parts[2:]
	default:
		collection = "/" + parts[0]
		parts = parts[1:]
	}
	if len(parts) > 0 {
		id = parts[0]
	}
	return collection, id
}

// supportedVerbs records which HTTP verbs ChurchTools actually serves for each
// collection. The mock used to answer every verb on every path, which meant an
// acceptance test could not tell a working endpoint from one that does not
// exist on a live instance: a resource built on a REST verb CT does not offer
// would still go green in CI.
//
// Add a collection here when you add a resource for it, with the verbs a real
// instance supports. An unregistered collection answers 404, exactly as CT does
// for a path it has no route for.
var supportedVerbs = map[string]map[string]bool{
	"/campuses": {
		http.MethodGet:    true,
		http.MethodPost:   true,
		http.MethodPut:    true,
		http.MethodDelete: true,
	},
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	collection, id := splitPath(r.URL.Path)

	verbs, known := supportedVerbs[collection]
	if !known {
		w.WriteHeader(http.StatusNotFound)
		writeData(w, map[string]any{
			"message": "unknown collection " + collection +
				" -- register it in testmock.supportedVerbs with the verbs a live instance supports",
		})
		return
	}
	if !verbs[r.Method] {
		w.WriteHeader(http.StatusMethodNotAllowed)
		writeData(w, map[string]any{
			"message": "ChurchTools serves no " + r.Method + " on " + collection,
		})
		return
	}

	if s.rows[collection] == nil {
		s.rows[collection] = map[string]any{}
	}

	switch {
	case r.Method == http.MethodGet && id == "":
		list := []any{}
		for _, v := range s.rows[collection] {
			list = append(list, v)
		}
		writeData(w, list)
	case r.Method == http.MethodGet:
		row, ok := s.rows[collection][id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeData(w, row)
	case r.Method == http.MethodPost:
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		s.nextID++
		body["id"] = float64(s.nextID)
		s.rows[collection][strconv.Itoa(s.nextID)] = body
		writeData(w, body)
	case r.Method == http.MethodPut || r.Method == http.MethodPatch:
		row, ok := s.rows[collection][id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		existing := row.(map[string]any)
		for k, v := range body {
			existing[k] = v
		}
		writeData(w, existing)
	case r.Method == http.MethodDelete:
		delete(s.rows[collection], id)
		writeData(w, map[string]any{})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func writeData(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"data": v})
}

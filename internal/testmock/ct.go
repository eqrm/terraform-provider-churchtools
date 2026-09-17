// Package testmock is an in-process ChurchTools stand-in. It holds rows in a
// map so acceptance tests exercise real CRUD round-trips with no network and
// no live instance — PR CI must never touch eqrm or eqrm-dev.
package testmock

import (
	"encoding/json"
	"fmt"
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
	"/group/grouptypes": {
		http.MethodGet:  true,
		http.MethodPost: true,
		http.MethodPut:  true,
	},
	"/statuses": {
		http.MethodGet:  true,
		http.MethodPost: true,
		http.MethodPut:  true,
	},
	"/person/commentviewers": {
		http.MethodGet:  true,
		http.MethodPost: true,
		http.MethodPut:  true,
	},
	// Bereiche are READ-ONLY over REST, and there is no GET by id either: CT
	// serves the collection and nothing else. Every write goes through the
	// legacy master-data endpoint below. Registering POST/PUT here would
	// re-open exactly the hole this table was added to close.
	"/departments": {
		http.MethodGet: true,
	},
	// The session handshake the legacy endpoint requires.
	"/whoami":    {http.MethodGet: true},
	"/csrftoken": {http.MethodGet: true},
}

// legacyPath is CT's non-REST endpoint, outside /api entirely.
const legacyPath = "/index.php"

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r.URL.Path == legacyPath {
		s.handleLegacy(w, r)
		return
	}

	collection, id := splitPath(r.URL.Path)

	switch collection {
	case "/whoami":
		// The handshake that buys the session cookie. ct-cli records that the
		// login_token must arrive as a QUERY PARAM: an Authorization header
		// yields a null CSRF token on a live instance.
		if r.URL.Query().Get("login_token") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			writeData(w, map[string]any{"message": "login_token query param required"})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "ChurchTools_ct", Value: "session-" + strconv.Itoa(s.nextID)})
		writeData(w, map[string]any{"id": 1, "firstName": "Test"})
		return
	case "/csrftoken":
		if r.Header.Get("Cookie") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			writeData(w, map[string]any{"message": "no session cookie"})
			return
		}
		writeData(w, "csrf-test-token")
		return
	}

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

// handleLegacy serves POST /index.php?q=<module>/ajax — form-encoded, and with
// a {status,data} envelope where a FAILURE is still HTTP 200. Tests that only
// checked the status code would pass against a broken call, so the mock models
// the envelope faithfully, including the "unknown function" error CT returns.
func (s *Server) handleLegacy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("CSRF-Token") == "" || r.Header.Get("Cookie") == "" {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{"status": "error", "message": "CSRF-Token is invalid"})
		return
	}
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	writeStatus := func(v map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": v})
	}
	writeErr := func(msg string) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "error", "message": msg})
	}

	switch r.Form.Get("func") {
	case "getMasterData":
		writeStatus(map[string]any{
			"masterDataTables": map[string]any{
				"7": map[string]any{
					"id":        7,
					"tablename": "cdb_bereich",
					"desc": map[string]any{
						"bezeichnung": map[string]any{"field": "bezeichnung"},
						"kuerzel":     map[string]any{"field": "kuerzel"},
						"sortkey":     map[string]any{"field": "sortkey"},
					},
				},
			},
		})
	case "saveMasterData":
		if r.Form.Get("table") != "cdb_bereich" {
			writeErr("unknown table " + r.Form.Get("table"))
			return
		}
		row := map[string]any{}
		for n := 0; ; n++ {
			col := r.Form.Get(fmt.Sprintf("col%d", n))
			if col == "" {
				break
			}
			row[col] = r.Form.Get(fmt.Sprintf("value%d", n))
		}
		// Translate the legacy column names back to the REST names the
		// collection read serves, so a write is visible to the next GET.
		rest := map[string]any{
			"name":   row["bezeichnung"],
			"shorty": row["kuerzel"],
		}
		if v, ok := row["sortkey"]; ok {
			n, _ := strconv.Atoi(fmt.Sprint(v))
			rest["sortKey"] = float64(n)
		}
		if s.rows["/departments"] == nil {
			s.rows["/departments"] = map[string]any{}
		}
		if id := r.Form.Get("id"); id != "" {
			existing, ok := s.rows["/departments"][id]
			if !ok {
				writeErr("no such row " + id)
				return
			}
			target := existing.(map[string]any)
			for k, v := range rest {
				target[k] = v
			}
		} else {
			s.nextID++
			rest["id"] = float64(s.nextID)
			s.rows["/departments"][strconv.Itoa(s.nextID)] = rest
		}
		writeStatus(nil)
	default:
		// CT validates function names rather than ignoring unknown ones.
		writeErr("Function " + r.Form.Get("func") + " was not defined as Function!")
	}
}

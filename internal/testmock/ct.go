// Package testmock is an in-process ChurchTools stand-in. It holds rows in a
// map so acceptance tests exercise real CRUD round-trips with no network and
// no live instance — PR CI must never touch eqrm or eqrm-dev.
package testmock

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Server struct {
	*httptest.Server
	mu     sync.Mutex
	rows   map[string]map[string]any // collection -> id -> row
	nextID int
	// dropCreateID makes POST answer without an `id`, reproducing a CT reply
	// the provider must refuse rather than store as "".
	dropCreateID map[string]bool
	// requireCookie, when set, makes every REST route answer 401 unless the
	// request carries exactly this cookie -- so a session-mode test proves the
	// credential actually authenticated, rather than that the mock ignored it.
	requireCookie string
	requireCSRF   string
	// legacyRows are extra getMasterData row sets (e.g.
	// "privacy_policy_agreement_types"), id -> row, served read-only.
	legacyRows map[string]map[string]any
}

func New() *Server {
	s := &Server{rows: map[string]map[string]any{}, nextID: 1000, dropCreateID: map[string]bool{}, legacyRows: map[string]map[string]any{}}
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

// RequireSession makes the mock behave like a ChurchTools instance that only
// accepts this session: every read must carry the cookie, and every write must
// additionally carry the CSRF token. A token in an Authorization header is not
// accepted, which is the point -- in session mode there is no token.
//
// The gate sits ABOVE the legacy dispatch, so /index.php is held to the same
// session as the REST routes. Below it, handleLegacy's own check -- which only
// asserts the two headers are non-empty -- would pass a stale cookie or the
// wrong CSRF token, and a session-mode department test would go green without
// proving anything. Departments are written only through that path.
func (s *Server) RequireSession(cookie, csrf string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requireCookie, s.requireCSRF = cookie, csrf
}

// SeedLegacy installs a row in a getMasterData row set. Values are stored as
// given; pass strings to reproduce the legacy payload, which stringifies numbers.
func (s *Server) SeedLegacy(set, id string, row map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.legacyRows[set] == nil {
		s.legacyRows[set] = map[string]any{}
	}
	row["id"] = id
	s.legacyRows[set][id] = row
}

// LegacyRows returns a copy of one getMasterData row set, so a test can assert
// what a legacy write actually changed.
func (s *Server) LegacyRows(set string) map[string]map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]map[string]any{}
	for id, v := range s.legacyRows[set] {
		row := map[string]any{}
		for k, val := range v.(map[string]any) {
			row[k] = val
		}
		out[id] = row
	}
	return out
}

// DropCreateID makes this collection's POST answer omit the id.
func (s *Server) DropCreateID(collection string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropCreateID[collection] = true
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
	// Both serve full CRUD on a live instance (CT 3.137 OpenAPI).
	"/contactlabels": {
		http.MethodGet:    true,
		http.MethodPost:   true,
		http.MethodPut:    true,
		http.MethodDelete: true,
	},
	"/person/relationshiptypes": {
		http.MethodGet:    true,
		http.MethodPost:   true,
		http.MethodPut:    true,
		http.MethodDelete: true,
	},
	// Bereiche are READ-ONLY over REST, and there is no GET by id either: CT
	// serves the collection and nothing else. Every write goes through the
	// legacy master-data endpoint below. Registering POST/PUT here would
	// re-open exactly the hole this table was added to close.
	"/departments": {
		http.MethodGet: true,
	},
	// Group master data. Every verb here was verified against a LIVE eqrm-dev
	// (CT 3.137.0-RC20) on 2026-09-26 rather than read off a spec: POST answers
	// 201 with data.id, DELETE 204. DELETE additionally REQUIRES an explicit
	// dryRun=TRUE|FALSE query parameter — omitting it is a 400, not a default —
	// which nothing here exercises, because every resource orphans on destroy.
	"/group/targetgroups": {
		http.MethodGet:    true,
		http.MethodPost:   true,
		http.MethodPut:    true, // 204, no body
		http.MethodDelete: true,
	},
	"/group/agegroups": {
		http.MethodGet:    true,
		http.MethodPost:   true,
		http.MethodPut:    true, // 204, no body
		http.MethodDelete: true,
	},
	"/group/groupcategories": {
		http.MethodGet:    true,
		http.MethodPost:   true,
		http.MethodPut:    true, // 200 WITH a body, unlike the two above
		http.MethodDelete: true,
	},
	// The session handshake the legacy endpoint requires.
	"/whoami":    {http.MethodGet: true},
	"/csrftoken": {http.MethodGet: true},
}

// collectionOnly lists collections CT serves ONLY as a whole: there is no
// GET /<collection>/{id}. Departments are the case that started this -- a Read
// that addresses one Bereich by id 404s on a live instance, and the resource
// must filter the collection instead.
var collectionOnly = map[string]bool{"/departments": true}

// legacyPath is CT's non-REST endpoint, outside /api entirely.
const legacyPath = "/index.php"

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.requireCookie != "" {
		if r.Header.Get("Cookie") != s.requireCookie {
			w.WriteHeader(http.StatusUnauthorized)
			writeData(w, map[string]any{"message": "no or wrong session cookie"})
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("CSRF-Token") != s.requireCSRF {
			w.WriteHeader(http.StatusUnauthorized)
			writeData(w, map[string]any{"message": "CSRF-Token is invalid"})
			return
		}
	}

	if r.URL.Path == legacyPath {
		s.handleLegacy(w, r)
		return
	}

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
	if id != "" && collectionOnly[collection] {
		w.WriteHeader(http.StatusNotFound)
		writeData(w, map[string]any{
			"message": "ChurchTools serves no " + collection + "/{id} -- filter the collection instead",
		})
		return
	}

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

	if s.rows[collection] == nil {
		s.rows[collection] = map[string]any{}
	}

	switch {
	case r.Method == http.MethodGet && id == "":
		// Ordered by id so paging is deterministic; CT pages list endpoints and
		// reports progress in meta.pagination, so the mock must too -- otherwise
		// a client that reads only page 1 still looks correct here.
		ids := make([]int, 0, len(s.rows[collection]))
		for k := range s.rows[collection] {
			n, _ := strconv.Atoi(k)
			ids = append(ids, n)
		}
		sort.Ints(ids)

		page, limit := pageParams(r)
		lastPage := 1
		if limit > 0 {
			lastPage = (len(ids) + limit - 1) / limit
			if lastPage == 0 {
				lastPage = 1
			}
		}
		lo := (page - 1) * limit
		hi := lo + limit
		if lo > len(ids) {
			lo = len(ids)
		}
		if hi > len(ids) {
			hi = len(ids)
		}
		list := []any{}
		for _, n := range ids[lo:hi] {
			list = append(list, s.rows[collection][strconv.Itoa(n)])
		}
		writeDataMeta(w, list, map[string]any{
			"pagination": map[string]any{"current": page, "lastPage": lastPage},
		})
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
		s.keepOneDefault(collection, strconv.Itoa(s.nextID), body)
		if s.dropCreateID[collection] {
			reply := map[string]any{}
			for k, v := range body {
				if k != "id" {
					reply[k] = v
				}
			}
			writeData(w, reply)
			return
		}
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
		s.keepOneDefault(collection, id, body)
		if putNoContent[collection] {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeData(w, existing)
	case r.Method == http.MethodDelete:
		delete(s.rows[collection], id)
		writeData(w, map[string]any{})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// putNoContent lists the collections whose live PUT answers 204 with an EMPTY
// body rather than the updated row. Answering those with a body here is what
// let an Update that choked on "" pass every test.
var putNoContent = map[string]bool{
	"/group/targetgroups": true,
	"/group/agegroups":    true,
}

// keepOneDefault models CT's single default contact label: writing
// isDefault=true on one label unsets it on every other.
func (s *Server) keepOneDefault(collection, id string, body map[string]any) {
	if collection != "/contactlabels" || body["isDefault"] != true {
		return
	}
	for other, row := range s.rows[collection] {
		if other != id {
			row.(map[string]any)["isDefault"] = false
		}
	}
}

// pageParams reads CT's ?page=&limit= pair, defaulting to one big page.
func pageParams(r *http.Request) (page, limit int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 1000
	}
	return page, limit
}

func writeDataMeta(w http.ResponseWriter, v any, meta map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"data": v, "meta": meta})
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
		payload := map[string]any{}
		for set, rows := range s.legacyRows {
			payload[set] = rows
		}
		payload["masterDataTables"] = map[string]any{
			"7": map[string]any{
				"id":        7,
				"tablename": "cdb_bereich",
				"desc": map[string]any{
					"bezeichnung": map[string]any{"field": "bezeichnung"},
					"kuerzel":     map[string]any{"field": "kuerzel"},
					"sortkey":     map[string]any{"field": "sortkey"},
				},
			},
			// Same columns as on both eqrm hosts (CT 3.137).
			"31": map[string]any{
				"id":        31,
				"tablename": "cdb_privacy_policy_agreement_types",
				"shortname": "privacy_policy_agreement_types",
				"desc": map[string]any{
					"id":          map[string]any{"field": "id"},
					"bezeichnung": map[string]any{"field": "bezeichnung"},
					"deletable":   map[string]any{"field": "deletable"},
					"sortkey":     map[string]any{"field": "sortkey"},
				},
			},
		}
		writeStatus(payload)
	case "saveMasterData":
		if r.Form.Get("table") == "cdb_privacy_policy_agreement_types" {
			s.savePrivacyAgreementType(r, writeStatus, writeErr)
			return
		}
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

// savePrivacyAgreementType mirrors CT's legacy write for
// cdb_privacy_policy_agreement_types: an empty id creates (and answers with NO
// id), a set id updates. Rows are stored the way getMasterData serves them —
// every value a string — and a new custom row is deletable, like on CT.
func (s *Server) savePrivacyAgreementType(r *http.Request, ok func(map[string]any), fail func(string)) {
	const set = "privacy_policy_agreement_types"
	cols := map[string]string{}
	for n := 0; ; n++ {
		col := r.Form.Get(fmt.Sprintf("col%d", n))
		if col == "" {
			break
		}
		cols[col] = r.Form.Get(fmt.Sprintf("value%d", n))
	}
	if s.legacyRows[set] == nil {
		s.legacyRows[set] = map[string]any{}
	}
	if id := r.Form.Get("id"); id != "" {
		existing, found := s.legacyRows[set][id]
		if !found {
			fail("no such row " + id)
			return
		}
		row := existing.(map[string]any)
		for k, v := range cols {
			row[k] = v
		}
		ok(nil)
		return
	}
	s.nextID++
	id := strconv.Itoa(s.nextID)
	row := map[string]any{"id": id, "deletable": "1", "sortkey": "0"}
	for k, v := range cols {
		row[k] = v
	}
	s.legacyRows[set][id] = row
	ok(nil)
}

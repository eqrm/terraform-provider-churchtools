package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
)

func newTestClient(h http.Handler) (*Client, func()) {
	srv := httptest.NewServer(h)
	return New(srv.URL, "tok"), srv.Close
}

func TestList_UnwrapsDataEnvelope(t *testing.T) {
	c, done := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Login tok" {
			t.Errorf("Authorization = %q, want %q", got, "Login tok")
		}
		w.Write([]byte(`{"data":[{"id":0,"name":"Mainz"},{"id":7,"name":"Idstein"}]}`))
	}))
	defer done()

	rows, err := c.List(context.Background(), "/campuses")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0]["name"] != "Mainz" {
		t.Errorf("rows[0][name] = %v, want Mainz", rows[0]["name"])
	}
}

// The Mainz campus is id 0 on prod. Any truthiness check on the id breaks
// exactly one resource, silently.
func TestGet_ZeroIDIsValid(t *testing.T) {
	c, done := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/campuses/0" {
			t.Errorf("path = %q, want /api/campuses/0", r.URL.Path)
		}
		w.Write([]byte(`{"data":{"id":0,"name":"Mainz"}}`))
	}))
	defer done()

	row, err := c.Get(context.Background(), "/campuses", "0")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row["name"] != "Mainz" {
		t.Errorf("name = %v, want Mainz", row["name"])
	}
}

func TestGet_NotFound(t *testing.T) {
	c, done := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer done()

	if _, err := c.Get(context.Background(), "/campuses", "99"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCreate_ReturnsCreatedRow(t *testing.T) {
	c, done := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		w.Write([]byte(`{"data":{"id":42,"name":"Neu"}}`))
	}))
	defer done()

	row, err := c.Create(context.Background(), "/campuses", Row{"name": "Neu"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if row["id"].(float64) != 42 {
		t.Errorf("id = %v, want 42", row["id"])
	}
}

func TestError_IncludesStatusAndBody(t *testing.T) {
	c, done := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"shorty is required"}`))
	}))
	defer done()

	_, err := c.Create(context.Background(), "/campuses", Row{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "422") || !strings.Contains(err.Error(), "shorty is required") {
		t.Errorf("err = %q, want it to mention 422 and the body", err.Error())
	}
}

// A 404 on a COLLECTION path means the endpoint does not exist on this
// instance, not that a row was deleted. Mapping it to ErrNotFound would make
// every resource of that type silently vanish from state and get re-created on
// the next apply. Only an item read may translate 404 into ErrNotFound.
func TestCollection404IsNotErrNotFound(t *testing.T) {
	always404 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"no route"}`))
	})

	c, done := newTestClient(always404)
	defer done()

	if _, err := c.List(context.Background(), "/departments"); err == nil {
		t.Fatal("List on a missing endpoint returned nil error")
	} else if errors.Is(err, ErrNotFound) {
		t.Errorf("List 404 mapped to ErrNotFound (%v); a missing endpoint must be a hard error", err)
	}

	// An item read keeps the existing contract: 404 means the row is gone.
	if _, err := c.Get(context.Background(), "/campuses", "7"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get 404 = %v, want ErrNotFound", err)
	}
}

// Get("") would hit the collection endpoint and decode an array into a Row,
// leaving the resource permanently unreadable. Reject it at the boundary.
func TestGet_EmptyIDIsRejected(t *testing.T) {
	c, done := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Get with an empty id reached the server at %s", r.URL.Path)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer done()

	if _, err := c.Get(context.Background(), "/campuses", ""); err == nil {
		t.Fatal("Get with empty id returned nil error")
	}
}

// CT pages its list endpoints and reports progress in meta.pagination. A List
// that reads only page 1 silently truncates the collection -- and departments
// resolve their id by filtering exactly this list, so a short read makes the
// duplicate-name guard pass and writes a second Bereich of the same name.
func TestList_FollowsPagination(t *testing.T) {
	var gotPaths []string
	c, done := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.RequestURI())
		switch r.URL.Query().Get("page") {
		case "", "1":
			w.Write([]byte(`{"data":[{"id":1},{"id":2}],"meta":{"pagination":{"current":1,"lastPage":3}}}`))
		case "2":
			w.Write([]byte(`{"data":[{"id":3},{"id":4}],"meta":{"pagination":{"current":2,"lastPage":3}}}`))
		default:
			w.Write([]byte(`{"data":[{"id":5}],"meta":{"pagination":{"current":3,"lastPage":3}}}`))
		}
	}))
	defer done()

	rows, err := c.List(context.Background(), "/departments")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("len(rows) = %d, want 5 across 3 pages; requests: %v", len(rows), gotPaths)
	}
	for i, want := range []float64{1, 2, 3, 4, 5} {
		if rows[i]["id"] != want {
			t.Errorf("rows[%d][id] = %v, want %v", i, rows[i]["id"], want)
		}
	}
}

// An endpoint that returns no pagination block is not a paged list; one request
// must be enough, with no endless follow-up pages.
func TestList_UnpagedEndpointReadsOnce(t *testing.T) {
	calls := 0
	c, done := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"data":[{"id":1}]}`))
	}))
	defer done()

	rows, err := c.List(context.Background(), "/campuses")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || calls != 1 {
		t.Errorf("rows=%d calls=%d, want 1 and 1", len(rows), calls)
	}
}

// One *client.Client is shared by every resource and Terraform applies
// resources concurrently, so the lazy legacy-session handshake runs from
// several goroutines at once. Guard it: before the mutex this raced on
// c.cookie/c.csrfToken under -race.
func TestSession_ConcurrentHandshakeIsSafe(t *testing.T) {
	ct := testmock.New()
	defer ct.Close()

	c := New(ct.URL, "tok")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.SaveMasterData(context.Background(), DepartmentTable,
				map[string]any{"bezeichnung": "Bereich", "kuerzel": "B", "sortkey": 0}, nil); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent SaveMasterData: %v", err)
	}
}

// --- session mode (ct auth token) -------------------------------------------

// The whole point of the mode: the permanent login token never reaches this
// process, so nothing may send it — and the session must authenticate the
// ordinary REST calls, not just the legacy ajax endpoint.
func TestSessionMode_SendsCookieAndNeverTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want none in session mode", got)
		}
		if got := r.Header.Get("Cookie"); got != "sid=abc" {
			t.Errorf("Cookie = %q, want %q", got, "sid=abc")
		}
		if got := r.Header.Get("CSRF-Token"); got != "" {
			t.Errorf("CSRF-Token = %q, want none on a GET", got)
		}
		w.Write([]byte(`{"data":[{"id":0,"name":"Mainz"}]}`))
	}))
	defer srv.Close()

	rows, err := NewWithSession(srv.URL, "sid=abc", "csrf-1").List(context.Background(), "/campuses")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
}

// A write without the CSRF header is answered by ChurchTools with a 401 that
// reads like an auth failure, so the header has to ride every non-GET.
func TestSessionMode_WritesCarryCSRF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("CSRF-Token"); got != "csrf-1" {
			t.Errorf("CSRF-Token = %q, want %q", got, "csrf-1")
		}
		w.Write([]byte(`{"data":{"id":7,"name":"Idstein"}}`))
	}))
	defer srv.Close()

	if _, err := NewWithSession(srv.URL, "sid=abc", "csrf-1").
		Create(context.Background(), "/campuses", Row{"name": "Idstein"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

// ct-cli calls its expiresAt a ceiling rather than a promise, so a 401 is the
// expected end of every session. The provider cannot renew one it was handed,
// so the error has to say what actually fixes it.
func TestSessionMode_ExpiredSessionSaysWhatToDo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Unauthorized"}`))
	}))
	defer srv.Close()

	_, err := NewWithSession(srv.URL, "sid=abc", "csrf-1").List(context.Background(), "/campuses")
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("err = %v, want ErrSessionExpired", err)
	}
	if !strings.Contains(err.Error(), "re-run") {
		t.Errorf("error does not name the remedy: %v", err)
	}
}

// The same 401 in TOKEN mode is an ordinary StatusError: that client CAN buy a
// new session, so telling its operator to re-run would be wrong advice.
func TestTokenMode_UnauthorizedStaysAStatusError(t *testing.T) {
	c, done := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer done()

	_, err := c.List(context.Background(), "/campuses")
	if errors.Is(err, ErrSessionExpired) {
		t.Fatalf("token mode must not report ErrSessionExpired: %v", err)
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want StatusError(401)", err)
	}
}

// A supplied session must never trigger the whoami/csrftoken handshake: there
// is no token to perform it with, and an attempt would fail the run.
func TestSessionMode_SkipsTheHandshake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s — session mode must not handshake", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	cookie, csrf, err := NewWithSession(srv.URL, "sid=abc", "csrf-1").session(context.Background())
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if cookie != "sid=abc" || csrf != "csrf-1" {
		t.Errorf("session() = (%q, %q), want the supplied pair", cookie, csrf)
	}
}

// Bereiche are written only through the legacy form-encoded endpoint, so that
// path has to work in session mode as well — and it is the one place the CSRF
// header was always required, token mode or not.
func TestSessionMode_LegacyAjaxUsesTheSuppliedSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/whoami" || r.URL.Path == "/api/csrftoken" {
			t.Fatalf("session mode must not handshake, got %s", r.URL.Path)
		}
		if got := r.Header.Get("Cookie"); got != "sid=abc" {
			t.Errorf("Cookie = %q, want %q", got, "sid=abc")
		}
		if got := r.Header.Get("CSRF-Token"); got != "csrf-1" {
			t.Errorf("CSRF-Token = %q, want %q", got, "csrf-1")
		}
		w.Write([]byte(`{"status":"success","data":{}}`))
	}))
	defer srv.Close()

	err := NewWithSession(srv.URL, "sid=abc", "csrf-1").
		Ajax(context.Background(), "churchdb", map[string]string{"func": "saveBereich"})
	if err != nil {
		t.Fatalf("Ajax: %v", err)
	}
}

// An expired session must say so on the LEGACY path too. Departments are
// written only through /index.php, so a remedy that reaches REST and not this
// endpoint never reaches the one resource that depends on it. A signed-out
// ChurchTools answers here with an HTML login page, which carries no envelope.
func TestSessionMode_ExpiredSessionOnLegacyPathSaysWhatToDo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`<!DOCTYPE html><html><body>Login</body></html>`))
	}))
	defer srv.Close()

	err := NewWithSession(srv.URL, "sid=abc", "csrf-1").
		Ajax(context.Background(), "churchdb", map[string]string{"func": "saveMasterData"})
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("error = %v, want ErrSessionExpired", err)
	}
	if !strings.Contains(err.Error(), "re-run") {
		t.Errorf("error %q does not say to re-run", err)
	}
	if strings.Contains(err.Error(), "undecodable") {
		t.Errorf("error %q still reports the login page as a decoding failure", err)
	}
}

// A non-2xx on the legacy path in TOKEN mode is a plain failure, not an expiry:
// that client can buy a new session, so "re-run" would be the wrong advice.
func TestTokenMode_LegacyUnauthorizedIsNotAnExpiry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/whoami":
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "server"})
			w.Write([]byte(`{"data":{"id":1}}`))
		case "/api/csrftoken":
			w.Write([]byte(`{"data":"csrf-server"}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"status":"error","message":"nope"}`))
		}
	}))
	defer srv.Close()

	err := New(srv.URL, "tok").Ajax(context.Background(), "churchdb", map[string]string{"func": "saveMasterData"})
	if err == nil {
		t.Fatal("want an error")
	}
	if errors.Is(err, ErrSessionExpired) {
		t.Errorf("token-mode 401 reported as an expiry: %v", err)
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error %q drops the instance's message", err)
	}
}

// A 200 carrying {"status":"error"} is still the envelope's call: the legacy
// endpoint reports a bad function name that way, and the status-code check
// added above it must not swallow that.
func TestAjax_EnvelopeErrorOnA200StillDecides(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"error","message":"Function nope was not defined as Function!"}`))
	}))
	defer srv.Close()

	err := NewWithSession(srv.URL, "sid=abc", "csrf-1").
		Ajax(context.Background(), "churchdb", map[string]string{"func": "nope"})
	if err == nil || !strings.Contains(err.Error(), "was not defined") {
		t.Fatalf("error = %v, want the envelope's message", err)
	}
}

// The 401 remedy must not cost the diagnosis. A cookie that never matched its
// CSRF token 401s exactly like an expiry, and re-running will not fix it --
// only the instance's own words distinguish the two.
func TestSessionMode_ExpiredSessionKeepsTheServersMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"CSRF-Token is invalid"}`))
	}))
	defer srv.Close()

	_, err := NewWithSession(srv.URL, "sid=abc", "csrf-1").Get(context.Background(), "/campuses", "1")
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("error = %v, want ErrSessionExpired", err)
	}
	if !strings.Contains(err.Error(), "CSRF-Token is invalid") {
		t.Errorf("error %q discards the server's diagnosis", err)
	}
}

// A half-specified session must not silently become token mode with an empty
// token: that sends `Authorization: Login ` and handshakes with an empty
// login_token, and the resulting 401 names neither cause.
func TestNewWithSession_CSRFWithoutCookieIsStillSessionMode(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/whoami" {
			t.Errorf("handshook with an empty token instead of using session mode")
		}
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"data":{"id":1}}`))
	}))
	defer srv.Close()

	c := NewWithSession(srv.URL, "", "csrf-1")
	if !c.usesSession() {
		t.Error("usesSession() = false for a session missing only its cookie")
	}
	if _, err := c.Get(context.Background(), "/campuses", "1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want none", gotAuth)
	}
}

// Configure DEFERS while a credential is unknown -- the normal state for the
// session pair -- and leaves the resource holding no client. Reaching CRUD that
// way must produce a diagnostic, not crash the plugin process.
func TestNilClient_IsAnErrorNotAPanic(t *testing.T) {
	var c *Client
	if _, err := c.Get(context.Background(), "/campuses", "1"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Get on a nil client: %v, want ErrNotConfigured", err)
	}
	if err := c.Ajax(context.Background(), "churchdb", map[string]string{"func": "saveMasterData"}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Ajax on a nil client: %v, want ErrNotConfigured", err)
	}
}

// CT answers some PUTs with 204 and no body at all. That is success; decoding
// the empty body as JSON must not turn it into an error.
func TestUpdate_NoContentIsSuccess(t *testing.T) {
	c, done := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer done()

	if _, err := c.Update(context.Background(), "/group/targetgroups", "1", http.MethodPut, Row{"name": "x"}); err != nil {
		t.Fatalf("err = %v, want nil for a 204", err)
	}
}

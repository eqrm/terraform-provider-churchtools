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

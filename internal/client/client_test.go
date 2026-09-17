package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

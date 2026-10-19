package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
)

// ChurchTools logs a login for every request that presents the token. A plan
// over ~340 objects must cost exactly one, however many resources run in
// parallel, and the REST calls must authenticate by the session instead.
func TestTokenMode_FullCycleInParallelIsOneLogin(t *testing.T) {
	ct := testmock.New()
	defer ct.Close()
	ct.StrictSessions()

	c := New(ct.URL, "tok")
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := context.Background()
			row, err := c.Create(ctx, "/campuses", Row{"name": fmt.Sprintf("c%d", i)})
			if err != nil {
				errs <- fmt.Errorf("create: %w", err)
				return
			}
			id := fmt.Sprint(int(row["id"].(float64)))
			if _, err := c.Get(ctx, "/campuses", id); err != nil {
				errs <- fmt.Errorf("get: %w", err)
			}
			if _, err := c.Update(ctx, "/campuses", id, http.MethodPut, Row{"name": "x"}); err != nil {
				errs <- fmt.Errorf("update: %w", err)
			}
			if _, err := c.List(ctx, "/campuses"); err != nil {
				errs <- fmt.Errorf("list: %w", err)
			}
			if err := c.Delete(ctx, "/campuses", id); err != nil {
				errs <- fmt.Errorf("delete: %w", err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := ct.Logins(); got != 1 {
		t.Errorf("logins = %d, want exactly 1", got)
	}
}

// A session that lapses mid-run is renewed once, shared by every request that
// noticed, and the refused request is retried.
func TestTokenMode_ExpiredSessionReauthenticatesOnce(t *testing.T) {
	ct := testmock.New()
	defer ct.Close()
	ct.StrictSessions()

	c := New(ct.URL, "tok")
	ctx := context.Background()
	if _, err := c.List(ctx, "/campuses"); err != nil {
		t.Fatalf("first List: %v", err)
	}
	ct.ExpireSessions()

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Create(ctx, "/campuses", Row{"name": "after"}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("after expiry: %v", err)
	}
	if got := ct.Logins(); got != 2 {
		t.Errorf("logins = %d, want 2 (initial + one renewal)", got)
	}
}

// A 401 that survives a fresh session is not an expiry: fail with a clear
// error after exactly one re-login, never loop.
func TestTokenMode_PersistentUnauthorizedFailsAfterOneRetry(t *testing.T) {
	var whoamis, rest int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/whoami":
			whoamis++
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "s"})
			w.Write([]byte(`{"data":{}}`))
		case "/api/csrftoken":
			w.Write([]byte(`{"data":"c"}`))
		default:
			rest++
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()

	_, err := New(srv.URL, "tok").List(context.Background(), "/campuses")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want a 401 StatusError", err)
	}
	if !strings.Contains(err.Error(), "freshly acquired session") {
		t.Errorf("error is not clear about the cause: %v", err)
	}
	if whoamis != 2 || rest != 2 {
		t.Errorf("whoami=%d rest=%d, want 2 and 2 (one retry, no loop)", whoamis, rest)
	}
}

// 429 on the login is waited out (Retry-After honoured), then succeeds.
func TestTokenMode_RateLimitedHandshakeIsRetried(t *testing.T) {
	ct := testmock.New()
	defer ct.Close()
	ct.StrictSessions()
	ct.RateLimitWhoami(2, "0")

	if _, err := New(ct.URL, "tok").List(context.Background(), "/campuses"); err != nil {
		t.Fatalf("List: %v", err)
	}
}

// A host that keeps refusing logins fails the run clearly, and the other
// resources do not each hammer it again.
func TestTokenMode_RateLimitedHandshakeGivesUp(t *testing.T) {
	ct := testmock.New()
	defer ct.Close()
	ct.RateLimitWhoami(1000, "0")

	c := New(ct.URL, "SECRET-TOKEN-VALUE")
	_, err := c.List(context.Background(), "/campuses")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want a 429 StatusError", err)
	}
	if !strings.Contains(err.Error(), "rate-limited") {
		t.Errorf("error does not say what happened: %v", err)
	}
	if _, err := c.List(context.Background(), "/campuses"); err == nil {
		t.Error("second call succeeded; want the remembered handshake failure")
	}
	if left := 1000 - remaining429(ct); left != handshakeAttempts {
		t.Errorf("whoami calls = %d, want %d (no retry storm)", left, handshakeAttempts)
	}
}

// remaining429 reads back how many 429s the mock still has queued.
func remaining429(ct *testmock.Server) int { return ct.Whoami429Left() }

// The token is a query parameter on /whoami and net/http quotes URLs in its
// errors, so a transport failure must not carry it into diagnostics.
func TestTokenMode_TokenNeverAppearsInErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // connection refused

	_, err := New(url, "SECRET-TOKEN-VALUE").List(context.Background(), "/campuses")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "SECRET-TOKEN-VALUE") {
		t.Errorf("token leaked into error: %v", err)
	}
}

// Session mode never logs in: the operator's session is the whole credential.
func TestSessionMode_NeverLogsIn(t *testing.T) {
	ct := testmock.New()
	defer ct.Close()
	ct.RequireSession("sid=abc", "csrf-1")

	c := NewWithSession(ct.URL, "sid=abc", "csrf-1")
	ctx := context.Background()
	row, err := c.Create(ctx, "/campuses", Row{"name": "x"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := c.Get(ctx, "/campuses", fmt.Sprint(int(row["id"].(float64)))); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := ct.Logins(); got != 0 {
		t.Errorf("logins = %d, want 0", got)
	}
}

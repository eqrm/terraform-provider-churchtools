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
	"time"

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

// A 502 on the login says nothing about the token: it must not be remembered
// for the run, the next caller retries.
func TestTokenMode_TransientHandshakeErrorIsNotCached(t *testing.T) {
	ct := testmock.New()
	defer ct.Close()
	ct.StrictSessions()
	ct.FailWhoami(1, http.StatusBadGateway)

	c := New(ct.URL, "tok")
	if _, err := c.List(context.Background(), "/campuses"); err == nil {
		t.Fatal("first call: want the 502")
	}
	if _, err := c.List(context.Background(), "/campuses"); err != nil {
		t.Fatalf("second call must retry the handshake: %v", err)
	}
	if got := ct.Logins(); got != 1 {
		t.Errorf("logins = %d, want 1", got)
	}
}

// A refused token is definitive and is remembered (no hammering).
func TestTokenMode_RefusedTokenIsCached(t *testing.T) {
	ct := testmock.New()
	defer ct.Close()
	ct.FailWhoami(1, http.StatusForbidden)

	c := New(ct.URL, "tok")
	for i := 0; i < 3; i++ {
		if _, err := c.List(context.Background(), "/campuses"); err == nil {
			t.Fatal("want an error")
		}
	}
	if got := ct.Logins(); got != 0 {
		t.Errorf("logins = %d, want 0 (the 403 is remembered, later calls do not retry)", got)
	}
}

// A route that 401s for a reason other than expiry may cost at most
// maxRenewals extra logins in total, however many requests hit it.
func TestTokenMode_RenewalsAreCapped(t *testing.T) {
	var mu sync.Mutex
	whoamis := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/whoami":
			whoamis++
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: fmt.Sprint("s", whoamis)})
			w.Write([]byte(`{"data":{}}`))
		case "/api/csrftoken":
			w.Write([]byte(`{"data":"c"}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	var last error
	for i := 0; i < 10; i++ {
		_, last = c.List(context.Background(), "/campuses")
	}
	if !errors.Is(last, ErrSessionKeepsExpiring) {
		t.Errorf("err = %v, want ErrSessionKeepsExpiring", last)
	}
	if whoamis != 1+maxRenewals {
		t.Errorf("logins = %d, want %d", whoamis, 1+maxRenewals)
	}
}

// Live CT answers a lapsed session on the legacy endpoint with 200 and
// {"status":"error","message":"Session expired!"}.
func TestTokenMode_AjaxSessionExpiredRenewsOnce(t *testing.T) {
	ct := testmock.New()
	defer ct.Close()
	ct.StrictSessions()

	c := New(ct.URL, "tok")
	ctx := context.Background()
	save := func() error {
		return c.SaveMasterData(ctx, DepartmentTable,
			map[string]any{"bezeichnung": "Bereich", "kuerzel": "B", "sortkey": 0}, nil)
	}
	if err := save(); err != nil {
		t.Fatalf("first save: %v", err)
	}
	ct.ExpireSessions()
	if err := save(); err != nil {
		t.Fatalf("save after expiry: %v", err)
	}
	if got := ct.Logins(); got != 2 {
		t.Errorf("logins = %d, want 2", got)
	}
}

// Cancelling while the login waits out a 429 returns promptly.
func TestTokenMode_CancelDuringRateLimitWait(t *testing.T) {
	ct := testmock.New()
	defer ct.Close()
	ct.RateLimitWhoami(1000, "30")

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	c := New(ct.URL, "tok")
	start := time.Now()
	_, err := c.List(ctx, "/campuses")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %v, want prompt return", time.Since(start))
	}

	// A waiter queued behind a slow handshake honours its own ctx too.
	go c.List(context.Background(), "/campuses")
	time.Sleep(50 * time.Millisecond)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	start = time.Now()
	if _, err := c.List(ctx2, "/campuses"); err == nil || time.Since(start) > 3*time.Second {
		t.Errorf("waiter: err=%v after %v, want prompt ctx error", err, time.Since(start))
	}
}

// csrftoken gets the same 429 patience as whoami.
func TestTokenMode_CSRFTokenRateLimitIsRetried(t *testing.T) {
	var mu sync.Mutex
	csrfCalls, whoamis := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/whoami":
			whoamis++
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "s"})
			w.Write([]byte(`{"data":{}}`))
		case "/api/csrftoken":
			csrfCalls++
			if csrfCalls == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.Write([]byte(`{"data":"c"}`))
		default:
			w.Write([]byte(`{"data":[]}`))
		}
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "tok").List(context.Background(), "/campuses"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if whoamis != 1 || csrfCalls != 2 {
		t.Errorf("whoami=%d csrftoken=%d, want 1 and 2", whoamis, csrfCalls)
	}
}

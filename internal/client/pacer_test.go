package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// session mode keeps the tests off the login handshake.
func newPacedClient(url string, rps float64) *Client {
	c := NewWithSession(url, "c=1", "csrf")
	c.SetMaxRequestsPerSecond(rps)
	c.retryBase = time.Millisecond
	return c
}

func okServer(hits *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{}}`))
	}))
}

func TestPacer_BurstStaysUnderRate(t *testing.T) {
	var hits atomic.Int32
	srv := okServer(&hits)
	defer srv.Close()
	c := newPacedClient(srv.URL, 50) // one start per 20ms

	const n = 21
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.do(context.Background(), http.MethodGet, "/x", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	// n requests at 50/s need at least (n-1)*20ms = 400ms; an unpaced burst takes a few ms.
	if el := time.Since(start); el < 380*time.Millisecond {
		t.Errorf("%d concurrent requests finished in %v, want >= 380ms at 50 req/s", n, el)
	}
	if hits.Load() != n {
		t.Errorf("hits = %d, want %d", hits.Load(), n)
	}
}

func TestRetry_429WithRetryAfterSucceeds(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= 2 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte("Too many requests"))
			return
		}
		w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer srv.Close()
	c := newPacedClient(srv.URL, 0)

	start := time.Now()
	// PUT with a body: the body must be resent on every attempt.
	if _, err := c.do(context.Background(), http.MethodPut, "/permissions/group_type_role/1", Row{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 3 {
		t.Errorf("hits = %d, want 3", hits.Load())
	}
	// The second 429 pauses again, so two Retry-After waits of 1s were honoured.
	if el := time.Since(start); el < 1900*time.Millisecond {
		t.Errorf("elapsed %v, want >= ~2s (Retry-After honoured)", el)
	}
}

// A 429 is about the client, not one request: it must hold siblings back too.
func TestRetry_429PausesSiblings(t *testing.T) {
	var hits atomic.Int32
	srv := okServer(&hits)
	defer srv.Close()
	c := newPacedClient(srv.URL, 0)

	c.pace.pauseFor(300 * time.Millisecond) // what a 429 on another request does
	start := time.Now()
	if _, err := c.do(context.Background(), http.MethodGet, "/b", nil); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el < 280*time.Millisecond {
		t.Errorf("request started %v after the pause began, want it held back ~300ms", el)
	}
}

func TestRetry_ExhaustedReturnsClearError(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("Too many requests"))
	}))
	defer srv.Close()
	c := newPacedClient(srv.URL, 0)

	_, err := c.do(context.Background(), http.MethodDelete, "/permissions/group_type_role/1", nil)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 429 {
		t.Errorf("err should wrap a 429 StatusError, got %v", err)
	}
	if got := hits.Load(); got != rateLimitRetries+1 {
		t.Errorf("hits = %d, want %d", got, rateLimitRetries+1)
	}
}

func TestRetry_ContextCancelAbortsWait(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := newPacedClient(srv.URL, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.do(ctx, http.MethodGet, "/x", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context deadline", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("cancel took %v, want prompt", el)
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d, want 1", hits.Load())
	}
}

func TestRetry_5xxIsNotRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := newPacedClient(srv.URL, 0)
	if _, err := c.do(context.Background(), http.MethodPut, "/x", Row{}); err == nil {
		t.Fatal("want error")
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d, want 1", hits.Load())
	}
}

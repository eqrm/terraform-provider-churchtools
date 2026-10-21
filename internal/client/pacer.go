package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// Request pacing and 429 retry for every call that leaves this client.
//
// ChurchTools publishes no number for its API rate limit. The only figures on
// record are from the sibling ct-cli (src/api/http.ts, src/auth/loginThrottle.ts),
// which settled on a default of 10 request starts per second per host, 6 retries
// after a 429 with 0.5s -> 16s exponential backoff, Retry-After honoured and each
// wait capped at 60s, and a pause shared by all requests of a host. The operator
// estimate quoted there for the instance is 20-30 requests a minute for logins,
// possibly 40. We reuse the ct-cli policy so both tools behave the same on one
// instance.
//
// Retrying alone does not keep a parallel apply under the limit: tofu runs 10
// resources at once, each one issuing a request per grant row, and every request
// that gets a 429 backing off on its own while its siblings keep firing runs out
// of retries before the instance lets anyone through. So all requests go through
// one pacer per Client (the provider shares one Client), which spaces request
// starts and holds ALL of them back when any one is told to slow down.

const (
	// DefaultMaxRequestsPerSecond is the provider's default request-start rate.
	DefaultMaxRequestsPerSecond = 10.0

	rateLimitRetries   = 6
	rateLimitBackoff   = 500 * time.Millisecond
	rateLimitMaxWait   = 60 * time.Second
	rateLimitJitterMax = 250 * time.Millisecond
)

// pacer spaces request starts and shares a 429 pause. A nil pointer does not
// pace.
type pacer struct {
	mu          sync.Mutex
	interval    time.Duration
	nextStart   time.Time
	pausedUntil time.Time
}

func newPacer(rps float64) *pacer {
	p := &pacer{}
	if rps > 0 {
		p.interval = time.Duration(float64(time.Second) / rps)
	}
	return p
}

// reserve claims the next start slot and returns how long to wait for it. It is
// synchronous so that N goroutines released at once claim N distinct slots
// before any of them sleeps, instead of starting in a burst.
func (p *pacer) reserve() time.Duration {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	start := now
	if p.nextStart.After(start) {
		start = p.nextStart
	}
	if p.pausedUntil.After(start) {
		start = p.pausedUntil
	}
	p.nextStart = start.Add(p.interval)
	return start.Sub(now)
}

// pauseFor holds every request back for d: a 429 is about the client, not one
// request.
func (p *pacer) pauseFor(d time.Duration) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if until := time.Now().Add(d); until.After(p.pausedUntil) {
		p.pausedUntil = until
	}
}

// wait blocks until this caller may start a request, or ctx is done.
func (p *pacer) wait(ctx context.Context) error {
	d := p.reserve()
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetMaxRequestsPerSecond sets the client-wide request-start rate. Zero or
// negative disables spacing (the shared 429 pause and retries stay). Call it
// before the first request; it is not safe against concurrent use.
func (c *Client) SetMaxRequestsPerSecond(rps float64) {
	c.pace = newPacer(rps)
}

// ErrRateLimited reports that ChurchTools kept answering 429 after every retry.
var ErrRateLimited = errors.New("churchtools: rate limited (HTTP 429)")

// send performs one HTTP exchange through the shared pacer, retrying a 429.
// `build` must return a fresh request each call (bodies are consumed).
//
// A 429 is safe to retry for every method, POSTs included: ChurchTools rejects
// it at the throttle before the request is processed (ct-cli relies on the same
// property), so nothing was applied. Only 429 is retried here; 5xx and
// transport errors may have been applied and are never repeated.
func (c *Client) send(ctx context.Context, build func() (*http.Request, error)) (int, []byte, error) {
	base := c.retryBase
	if base <= 0 {
		base = rateLimitBackoff
	}
	for attempt := 0; ; attempt++ {
		if err := c.pace.wait(ctx); err != nil {
			return 0, nil, err
		}
		req, err := build()
		if err != nil {
			return 0, nil, err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return 0, nil, err
		}
		raw, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusTooManyRequests {
			return resp.StatusCode, raw, rerr
		}
		if attempt >= rateLimitRetries {
			return 0, nil, fmt.Errorf("%w: %s %s still refused after %d retries; ChurchTools is "+
				"throttling this client, lower max_requests_per_second or retry later: %w",
				ErrRateLimited, req.Method, req.URL.Path, rateLimitRetries,
				&StatusError{Method: req.Method, Path: req.URL.Path, Status: resp.StatusCode, Body: string(raw)})
		}
		backoff := base << attempt
		backoff += time.Duration(rand.Int64N(int64(rateLimitJitterMax)))
		d := retryAfterMax(resp.Header.Get("Retry-After"), backoff, rateLimitMaxWait)
		c.pace.pauseFor(d)
		// The next loop's pace.wait sleeps through the pause (ctx-aware).
	}
}

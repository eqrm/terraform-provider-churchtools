package client

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
)

const deadlockBody = `{"message":"Churchtools\\Lib\\DB\\SQLException: A server error occurred","translatedMessage":"server.error","errors":[{"message":"SQLSTATE[40001]: Serialization failure: 1213 Deadlock found when trying to get lock; try restarting transaction"}]}`

func deadlockClient(url string) *Client {
	c := NewWithSession(url, "c=1", "csrf")
	c.SetMaxRequestsPerSecond(0)
	return c
}

func TestDeadlock_RetriedUntilSuccess(t *testing.T) {
	ct := testmock.New()
	defer ct.Close()
	ct.FailNext(2, http.StatusInternalServerError, deadlockBody)
	if _, err := deadlockClient(ct.URL).List(context.Background(), "/campuses"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := ct.Requests(); got != 3 {
		t.Errorf("requests = %d, want 3 (2 deadlocks + success)", got)
	}
}

func TestDeadlock_OtherServerErrorNotRetried(t *testing.T) {
	ct := testmock.New()
	defer ct.Close()
	ct.FailNext(5, http.StatusInternalServerError, `{"message":"boom"}`)
	_, err := deadlockClient(ct.URL).List(context.Background(), "/campuses")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 500 {
		t.Fatalf("err = %v, want a 500 StatusError", err)
	}
	if got := ct.Requests(); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

func TestDeadlock_GivesUpAfterMaxRetries(t *testing.T) {
	ct := testmock.New()
	defer ct.Close()
	ct.FailNext(1000, http.StatusInternalServerError, deadlockBody)
	_, err := deadlockClient(ct.URL).List(context.Background(), "/campuses")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 500 {
		t.Fatalf("err = %v, want a 500 StatusError", err)
	}
	if got := ct.Requests(); got != 1+deadlockRetries {
		t.Errorf("requests = %d, want %d", got, 1+deadlockRetries)
	}
}

func TestDeadlock_CancelDuringBackoff(t *testing.T) {
	ct := testmock.New()
	defer ct.Close()
	ct.FailNext(1000, http.StatusInternalServerError, deadlockBody)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := deadlockClient(ct.URL).List(ctx, "/campuses")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("cancellation was not prompt")
	}
}

func TestDeadlock_PostNotRetried(t *testing.T) {
	ct := testmock.New()
	defer ct.Close()
	ct.FailNext(1, http.StatusInternalServerError, deadlockBody)
	_, err := deadlockClient(ct.URL).Create(context.Background(), "/campuses", Row{"name": "x"})
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 500 {
		t.Fatalf("err = %v, want a 500 StatusError", err)
	}
	if got := ct.Requests(); got != 1 {
		t.Errorf("requests = %d, want 1 (a POST may have been partly applied)", got)
	}
}

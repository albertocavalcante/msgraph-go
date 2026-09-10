package msgraph

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The tests in this file close assertion gaps that mutation testing exposed:
// each one fails if a boundary or condition in the code under test is flipped.

func TestAPIErrorMessageFormatting(t *testing.T) {
	tests := []struct {
		name    string
		err     APIError
		want    string
		absent  string
		present []string
	}{
		{
			name:    "code and message",
			err:     APIError{StatusCode: 404, Code: "ErrorItemNotFound", Message: "not found"},
			present: []string{"404", "ErrorItemNotFound", "not found"},
		},
		{
			name:    "code only",
			err:     APIError{StatusCode: 403, Code: "ErrorAccessDenied"},
			present: []string{"403", "ErrorAccessDenied"},
		},
		{
			name:    "message only",
			err:     APIError{StatusCode: 400, Message: "bad request"},
			present: []string{"400", "bad request"},
		},
		{
			name:    "neither",
			err:     APIError{StatusCode: 500},
			want:    "msgraph: request failed: status 500",
			present: []string{"500"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if tt.want != "" && got != tt.want {
				t.Fatalf("Error() = %q, want %q", got, tt.want)
			}
			for _, want := range tt.present {
				if !strings.Contains(got, want) {
					t.Fatalf("Error() = %q, want it to contain %q", got, want)
				}
			}
			// A missing field must not leave a stray separator behind.
			if strings.Contains(got, ": :") || strings.HasSuffix(got, ": ") {
				t.Fatalf("Error() = %q, has a dangling separator", got)
			}
		})
	}
}

func TestWithMaxRetriesIsHonored(t *testing.T) {
	tests := []struct {
		name         string
		maxRetries   int
		wantAttempts int
	}{
		{"no retries", 0, 1},
		{"two retries", 2, 3},
		{"five retries", 5, 6},
		{"negative clamps to none", -3, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var attempts int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer server.Close()

			client, err := New(staticToken("t"),
				WithBaseURL(server.URL),
				WithMaxRetries(tt.maxRetries),
				WithSleeper(func(context.Context, time.Duration) error { return nil }),
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Get(context.Background(), "/me", Params{}, nil); err == nil {
				t.Fatal("expected the 503 to surface")
			}
			if attempts != tt.wantAttempts {
				t.Fatalf("attempts = %d, want %d", attempts, tt.wantAttempts)
			}
		})
	}
}

// Mutating methods must not be retried by default, because Graph can have
// already applied the write before the transient response was observed.
func TestUnsafeMethodsAreNotRetriedByDefault(t *testing.T) {
	for _, tt := range []struct {
		name         string
		opts         []Option
		wantAttempts int
	}{
		{"default", nil, 1},
		{"opted in", []Option{WithRetryUnsafeMethods(true)}, 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var attempts int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer server.Close()

			opts := append([]Option{
				WithBaseURL(server.URL),
				WithMaxRetries(2),
				WithSleeper(func(context.Context, time.Duration) error { return nil }),
			}, tt.opts...)
			client, err := New(staticToken("t"), opts...)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Post(context.Background(), "/me/sendMail", Params{}, map[string]string{}, nil); err == nil {
				t.Fatal("expected the 503 to surface")
			}
			if attempts != tt.wantAttempts {
				t.Fatalf("attempts = %d, want %d", attempts, tt.wantAttempts)
			}
		})
	}
}

// The batch limit is inclusive: exactly 20 is legal, 21 is not.
func TestBatchSizeBoundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"responses":[]}`))
	}))
	defer server.Close()

	client, err := New(staticToken("t"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	build := func(n int) []BatchRequest {
		requests := make([]BatchRequest, n)
		for i := range requests {
			requests[i] = BatchRequest{ID: fmt.Sprint(i), Method: http.MethodGet, URL: "/me"}
		}
		return requests
	}

	if _, err := client.Batch(context.Background(), build(maxBatchRequests)); err != nil {
		t.Fatalf("batch of exactly %d was rejected: %v", maxBatchRequests, err)
	}
	if _, err := client.Batch(context.Background(), build(maxBatchRequests+1)); err == nil {
		t.Fatalf("batch of %d was accepted", maxBatchRequests+1)
	}
	if _, err := client.Batch(context.Background(), nil); err == nil {
		t.Fatal("empty batch was accepted")
	}
}

// 2xx is success inclusive of both ends; 199 and 300 are not.
func TestFailedBatchResponsesStatusBoundaries(t *testing.T) {
	responses := []BatchResponse{
		{ID: "a", Status: 199},
		{ID: "b", Status: 200},
		{ID: "c", Status: 299},
		{ID: "d", Status: 300},
	}
	failed := FailedBatchResponses(responses)
	if len(failed) != 2 {
		t.Fatalf("failed = %+v, want exactly the 199 and 300 entries", failed)
	}
	if failed[0].ID != "a" || failed[1].ID != "d" {
		t.Fatalf("failed = %+v, want ids a and d", failed)
	}
}

// First asks for a single item by default, but must not override an explicit
// page size the caller chose.
func TestFirstRequestsOneItemByDefault(t *testing.T) {
	tests := []struct {
		name    string
		params  Params
		wantTop string
	}{
		{"default", Params{}, "1"},
		{"explicit top preserved", Params{Top: 25}, "25"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = r.URL.Query().Get("$top")
				_, _ = w.Write([]byte(`{"value":[{"id":"1"}]}`))
			}))
			defer server.Close()

			client, err := New(staticToken("t"), WithBaseURL(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			_, found, err := First[testMessage](context.Background(), client, "/me/messages", tt.params)
			if err != nil || !found {
				t.Fatalf("found = %v, err = %v", found, err)
			}
			if seen != tt.wantTop {
				t.Fatalf("$top = %q, want %q", seen, tt.wantTop)
			}
		})
	}
}

// "Retry-After: 0" means retry immediately. It is a valid delay-seconds value,
// so it must be taken as zero and floored to the base delay, not treated as
// unparseable and replaced by a full exponential backoff.
func TestRetryAfterZeroFloorsToBaseDelay(t *testing.T) {
	header := http.Header{"Retry-After": []string{"0"}}
	if got := retryDelay(header, 6, 30*time.Second); got != backoffBase {
		t.Fatalf("retryDelay = %v, want the base delay %v", got, backoffBase)
	}
}

// Jitter is added to the backoff, never subtracted: the delay must never fall
// below the base for the first attempt.
func TestBackoffJitterOnlyAdds(t *testing.T) {
	for range 200 {
		delay := uncappedRetryDelay(http.Header{}, 0)
		if delay < backoffBase {
			t.Fatalf("delay = %v, want at least the base %v", delay, backoffBase)
		}
		if delay >= backoffBase+backoffJitter {
			t.Fatalf("delay = %v, want less than %v", delay, backoffBase+backoffJitter)
		}
	}
}

// A zero maxDelay disables the cap rather than clamping every delay to zero.
func TestZeroMaxDelayDisablesTheCap(t *testing.T) {
	uncapped := retryDelay(http.Header{"Retry-After": []string{"45"}}, 0, 0)
	if uncapped != 45*time.Second {
		t.Fatalf("retryDelay with no cap = %v, want 45s", uncapped)
	}
	capped := retryDelay(http.Header{"Retry-After": []string{"45"}}, 0, 5*time.Second)
	if capped != 5*time.Second {
		t.Fatalf("retryDelay with a 5s cap = %v, want 5s", capped)
	}
}

// BatchStrict must only report an error when a subrequest actually failed.
func TestBatchStrictSucceedsWhenNothingFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"responses":[{"id":"a","status":200},{"id":"b","status":204}]}`))
	}))
	defer server.Close()

	client, err := New(staticToken("t"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	responses, err := client.BatchStrict(context.Background(), []BatchRequest{
		{ID: "a", Method: http.MethodGet, URL: "/me"},
		{ID: "b", Method: http.MethodGet, URL: "/me"},
	})
	if err != nil {
		t.Fatalf("BatchStrict = %v, want nil for an all-success batch", err)
	}
	if len(responses) != 2 {
		t.Fatalf("responses = %+v", responses)
	}
}

// The whole 2xx range is success, inclusive of the unusual upper end.
func TestSuccessStatusBoundaries(t *testing.T) {
	for _, status := range []int{200, 201, 204, 299} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"id":"1"}`))
			}))
			defer server.Close()

			client, err := New(staticToken("t"), WithBaseURL(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := Get[testMessage](context.Background(), client, "/me", Params{}); err != nil {
				t.Fatalf("status %d treated as an error: %v", status, err)
			}
		})
	}
	for _, status := range []int{300, 400} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()

			client, err := New(staticToken("t"), WithBaseURL(server.URL), WithMaxRetries(0))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := Get[testMessage](context.Background(), client, "/me", Params{}); err == nil {
				t.Fatalf("status %d treated as success", status)
			}
		})
	}
}

// No Prefer header should be sent when the caller asked for no directives; an
// empty one is noise the service has to parse.
func TestNoPreferHeaderWhenUnset(t *testing.T) {
	var present bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header["Prefer"]
		_, _ = w.Write([]byte(`{"id":"1"}`))
	}))
	defer server.Close()

	client, err := New(staticToken("t"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(context.Background(), "/me", Params{}, nil); err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("a Prefer header was sent with no directives")
	}
}

// The default transport has to carry real timeouts: a zero Timeout means a
// hung Graph request never returns.
func TestDefaultHTTPClientHasTimeouts(t *testing.T) {
	client := defaultHTTPClient()
	if client.Timeout != 60*time.Second {
		t.Fatalf("Timeout = %v, want 60s", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
	}
	checks := map[string]time.Duration{
		"IdleConnTimeout":       transport.IdleConnTimeout,
		"TLSHandshakeTimeout":   transport.TLSHandshakeTimeout,
		"ResponseHeaderTimeout": transport.ResponseHeaderTimeout,
		"ExpectContinueTimeout": transport.ExpectContinueTimeout,
	}
	for name, value := range checks {
		if value <= 0 {
			t.Errorf("%s = %v, want a positive timeout", name, value)
		}
	}
	if transport.MaxIdleConns <= 0 {
		t.Errorf("MaxIdleConns = %d, want positive", transport.MaxIdleConns)
	}
	if !transport.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 = false, want HTTP/2 attempted")
	}
}

// Only the transient statuses are retried; a 400 or a 404 must surface at once.
func TestRetryableStatuses(t *testing.T) {
	retryable := map[int]bool{
		http.StatusTooManyRequests:     true,
		http.StatusBadGateway:          true,
		http.StatusServiceUnavailable:  true,
		http.StatusGatewayTimeout:      true,
		http.StatusInternalServerError: false,
		http.StatusNotFound:            false,
		http.StatusBadRequest:          false,
		http.StatusOK:                  false,
		http.StatusNotImplemented:      false,
	}
	for status, want := range retryable {
		if got := retryableStatus(status); got != want {
			t.Errorf("retryableStatus(%d) = %v, want %v", status, got, want)
		}
	}
}

// The success path had no size limit while the error path did, so a large or
// hostile response was an unbounded allocation. Measured before this bound
// existed: a 512 MiB body cost 2.2 GiB of heap.
func TestResponseSizeIsBounded(t *testing.T) {
	const limit = 1 << 20 // 1 MiB, to keep the test quick

	serve := func(size int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"`))
			_, _ = w.Write(bytes.Repeat([]byte("x"), size))
			_, _ = w.Write([]byte(`"}`))
		}))
	}

	t.Run("over the limit is refused", func(t *testing.T) {
		server := serve(limit * 2)
		defer server.Close()

		client, err := New(staticToken("t"), WithBaseURL(server.URL), WithMaxResponseBytes(limit))
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = Get[testMessage](context.Background(), client, "/me", Params{})
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("err = %v, want ErrResponseTooLarge", err)
		}
	})

	// The other half of the bound: it must not fire on legitimate payloads.
	t.Run("under the limit still decodes", func(t *testing.T) {
		server := serve(limit / 2)
		defer server.Close()

		client, err := New(staticToken("t"), WithBaseURL(server.URL), WithMaxResponseBytes(limit))
		if err != nil {
			t.Fatal(err)
		}
		message, _, err := Get[testMessage](context.Background(), client, "/me", Params{})
		if err != nil {
			t.Fatalf("a legitimate payload was refused: %v", err)
		}
		if len(message.ID) != limit/2 {
			t.Fatalf("id length = %d, want %d", len(message.ID), limit/2)
		}
	})

	t.Run("the default admits a realistic page of mail", func(t *testing.T) {
		// A full Graph page with bodies is single-digit megabytes; the default
		// has to sit comfortably above that.
		if defaultMaxResponseBytes < 32<<20 {
			t.Fatalf("defaultMaxResponseBytes = %d, too small for a real page", defaultMaxResponseBytes)
		}
	})

	t.Run("streaming is not subject to the limit", func(t *testing.T) {
		server := serve(limit * 2)
		defer server.Close()

		client, err := New(staticToken("t"), WithBaseURL(server.URL), WithMaxResponseBytes(limit))
		if err != nil {
			t.Fatal(err)
		}
		var sink bytes.Buffer
		if _, err := client.Get(context.Background(), "/me", Params{}, &sink); err != nil {
			t.Fatalf("streaming was refused: %v", err)
		}
		if sink.Len() <= limit {
			t.Fatalf("streamed %d bytes, want more than the limit", sink.Len())
		}
	})

	t.Run("the limit can be removed", func(t *testing.T) {
		server := serve(limit * 2)
		defer server.Close()

		client, err := New(staticToken("t"), WithBaseURL(server.URL), WithMaxResponseBytes(0))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Get[testMessage](context.Background(), client, "/me", Params{}); err != nil {
			t.Fatalf("err = %v, want the limit disabled", err)
		}
	})
}

package msgraph

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A Client is documented as reusable, and callers will share one. Nothing
// previously exercised that, so the claim was untested. Run with -race, which
// the CI gate does.
func TestClientIsSafeForConcurrentUse(t *testing.T) {
	var served atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		_, _ = fmt.Fprintf(w, `{"id":%q,"subject":"s"}`, r.URL.Query().Get("id"))
	}))
	defer server.Close()

	client, err := New(staticToken("t"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}

	const goroutines, each = 24, 20
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*each)

	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				want := fmt.Sprintf("%d-%d", g, i)
				got, _, err := Get[testMessage](context.Background(), client, "/me/messages",
					Params{Custom: map[string][]string{"id": {want}}})
				if err != nil {
					errs <- err
					return
				}
				// Responses must not be crossed between goroutines.
				if got.ID != want {
					errs <- fmt.Errorf("got id %q, want %q", got.ID, want)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}
	if got := served.Load(); got != goroutines*each {
		t.Fatalf("served %d requests, want %d", got, goroutines*each)
	}
}

// Retrying shares the sleeper and the retry counters across callers.
func TestConcurrentRetriesDoNotInterfere(t *testing.T) {
	// Count attempts per caller rather than globally: a global parity check
	// is not deterministic under concurrency, and a flaky test proves nothing.
	var mu sync.Mutex
	attemptsFor := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller := r.URL.Query().Get("caller")
		mu.Lock()
		attemptsFor[caller]++
		attempt := attemptsFor[caller]
		mu.Unlock()

		// Fail each caller's first two attempts, succeed on the third.
		if attempt <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"id":"1"}`))
	}))
	defer server.Close()

	client, err := New(staticToken("t"),
		WithBaseURL(server.URL),
		WithMaxRetries(4),
		WithSleeper(func(context.Context, time.Duration) error { return nil }),
	)
	if err != nil {
		t.Fatal(err)
	}

	const callers = 16
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			caller := fmt.Sprint(i)
			if _, _, err := Get[testMessage](context.Background(), client, "/me",
				Params{Custom: map[string][]string{"caller": {caller}}}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	// Every caller retried independently: three attempts each, no more.
	mu.Lock()
	defer mu.Unlock()
	if len(attemptsFor) != callers {
		t.Fatalf("saw %d distinct callers, want %d", len(attemptsFor), callers)
	}
	for caller, attempts := range attemptsFor {
		if attempts != 3 {
			t.Fatalf("caller %s made %d attempts, want 3", caller, attempts)
		}
	}
}

// Concurrent pagination must not share iterator state.
func TestConcurrentPaginationIsIsolated(t *testing.T) {
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stream := r.URL.Query().Get("stream")
		page := r.URL.Query().Get("page")
		if page == "2" {
			_, _ = fmt.Fprintf(w, `{"value":[{"id":"%s-b"}]}`, stream)
			return
		}
		_, _ = fmt.Fprintf(w, `{"value":[{"id":"%s-a"}],"@odata.nextLink":%q}`,
			stream, fmt.Sprintf("%s/me/messages?stream=%s&page=2", base, stream))
	}))
	defer server.Close()
	base = server.URL

	client, err := New(staticToken("t"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream := fmt.Sprint(i)
			items, err := Collect[testMessage](context.Background(), client, "/me/messages",
				Params{Custom: map[string][]string{"stream": {stream}}})
			if err != nil {
				errs <- err
				return
			}
			if len(items) != 2 {
				errs <- fmt.Errorf("stream %s got %d items", stream, len(items))
				return
			}
			// Each stream must see only its own pages.
			for _, item := range items {
				if !strings.HasPrefix(item.ID, stream+"-") {
					errs <- fmt.Errorf("stream %s saw %q", stream, item.ID)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// Canceling during a traversal must stop promptly rather than draining the
// whole collection, and must report the cancellation.
func TestCancellationStopsPagingPromptly(t *testing.T) {
	var served atomic.Int64
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := served.Add(1)
		_, _ = fmt.Fprintf(w, `{"value":[{"id":"%d"}],"@odata.nextLink":%q}`,
			n, fmt.Sprintf("%s/p/%d", base, n))
	}))
	defer server.Close()
	base = server.URL

	client, err := New(staticToken("t"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var seen int
	var lastErr error
	for _, err := range Items[testMessage](ctx, client, "/me/messages", Params{}) {
		if err != nil {
			lastErr = err
			break
		}
		seen++
		if seen == 3 {
			cancel()
		}
	}
	cancel()

	if !errors.Is(lastErr, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", lastErr)
	}
	// It must not have kept fetching after the cancel.
	if got := served.Load(); got > int64(seen)+2 {
		t.Fatalf("served %d pages after canceling at %d items", got, seen)
	}
}

// A canceled context must be honored before the request is sent, not after.
func TestAlreadyCancelledContextMakesNoRequest(t *testing.T) {
	var served atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		_, _ = w.Write([]byte(`{"id":"1"}`))
	}))
	defer server.Close()

	client, err := New(staticToken("t"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := Get[testMessage](ctx, client, "/me", Params{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := served.Load(); got != 0 {
		t.Fatalf("served %d requests with a canceled context", got)
	}
}

// A server that sends headers and then stalls must not hang the caller
// forever: the context deadline has to win.
func TestStalledResponseBodyRespectsTheDeadline(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			_, _ = w.Write([]byte(`{"id":"`))
			flusher.Flush()
		}
		<-release // Stall mid-body.
	}))
	defer server.Close()
	defer close(release)

	client, err := New(staticToken("t"), WithBaseURL(server.URL), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err = Get[testMessage](ctx, client, "/me", Params{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a stalled body returned successfully")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("took %v to give up on a stalled body", elapsed)
	}
}

// A truncated body is a decode error, not a panic and not a silent zero value.
func TestTruncatedBodyIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "128")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"truncated`))
		// Close without completing the declared length.
		if hijacker, ok := w.(http.Hijacker); ok {
			conn, _, hijackErr := hijacker.Hijack()
			if hijackErr == nil {
				_ = conn.Close()
			}
		}
	}))
	defer server.Close()

	client, err := New(staticToken("t"), WithBaseURL(server.URL), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := Get[testMessage](context.Background(), client, "/me", Params{})
	if err == nil {
		t.Fatalf("a truncated body decoded to %+v", message)
	}
}

// Malformed JSON must surface as an error carrying context, never a panic.
func TestMalformedBodiesDoNotPanic(t *testing.T) {
	bodies := []string{
		"", "{", "[", "null", "true", `{"value":`, "\x00\x01\x02",
		`{"value":[{"id":`, strings.Repeat("[", 2000), `{"id":{"nested":"wrongtype"}}`,
	}

	for i, body := range bodies {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()

			client, err := New(staticToken("t"), WithBaseURL(server.URL), WithMaxRetries(0))
			if err != nil {
				t.Fatal(err)
			}
			// The assertion is simply that this returns rather than panics.
			_, _, _ = Get[testMessage](context.Background(), client, "/me", Params{})
		})
	}
}

// The token source is consulted on every request, including concurrently and
// on each retry. A failure there must surface rather than send an empty token.
func TestTokenSourceFailureSurfaces(t *testing.T) {
	var served atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		_, _ = w.Write([]byte(`{"id":"1"}`))
	}))
	defer server.Close()

	wantErr := errors.New("token unavailable")
	client, err := New(TokenSourceFunc(func(context.Context) (string, error) {
		return "", wantErr
	}), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = Get[testMessage](context.Background(), client, "/me", Params{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want the token error", err)
	}
	if got := served.Load(); got != 0 {
		t.Fatalf("served %d requests without a token", got)
	}
}

// A token source used concurrently must not be serialized incorrectly or
// produce crossed tokens.
func TestConcurrentTokenSourceUse(t *testing.T) {
	var counter atomic.Int64
	var mismatches atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer token-") {
			mismatches.Add(1)
		}
		_, _ = w.Write([]byte(`{"id":"1"}`))
	}))
	defer server.Close()

	client, err := New(TokenSourceFunc(func(context.Context) (string, error) {
		return fmt.Sprintf("token-%d", counter.Add(1)), nil
	}), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = Get[testMessage](context.Background(), client, "/me", Params{})
		}()
	}
	wg.Wait()

	if got := mismatches.Load(); got != 0 {
		t.Fatalf("%d requests carried a malformed token", got)
	}
}

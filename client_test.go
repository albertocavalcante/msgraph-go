package msgraph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientGetAddsAuthAndQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.URL.Path; got != "/v1.0/me/messages" {
			t.Fatalf("path = %q", got)
		}
		if got := r.URL.Query().Get("$select"); got != "id,subject" {
			t.Fatalf("$select = %q", got)
		}
		if got := r.URL.Query().Get("$top"); got != "2" {
			t.Fatalf("$top = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"id":"1","subject":"hello"}]}`))
	}))
	defer server.Close()

	client, err := New(staticToken("test-token"), WithBaseURL(server.URL+"/v1.0"))
	if err != nil {
		t.Fatal(err)
	}
	var page Page[testMessage]
	if _, err := client.Get(context.Background(), "/me/messages", Params{
		Select: []string{"id", "subject"},
		Top:    2,
	}, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Value) != 1 || page.Value[0].Subject != "hello" {
		t.Fatalf("page = %+v", page)
	}
}

func TestClientRequestHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Values("Prefer"); len(got) != 1 || got[0] != `IdType="ImmutableId", outlook.body-content-type="text"` {
			t.Fatalf("Prefer = %q", got)
		}
		if got := r.Header.Get("ConsistencyLevel"); got != ConsistencyLevelEventual {
			t.Fatalf("ConsistencyLevel = %q", got)
		}
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer server.Close()

	client, err := New(staticToken("test-token"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	var out testMessage
	_, err = client.Do(context.Background(), Request{
		Method:           http.MethodGet,
		URL:              "/me/messages/1",
		Prefer:           []string{PreferIDTypeImmutableID, PreferBodyContentTypeText},
		ConsistencyLevel: ConsistencyLevelEventual,
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
}

func TestClientPostJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["subject"] != "hello" {
			t.Fatalf("body = %+v", body)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client, err := New(staticToken("test-token"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Post(context.Background(), "/me/sendMail", Params{}, map[string]string{"subject": "hello"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("StatusCode = %d", resp.StatusCode)
	}
}

func TestClientAbsoluteURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/next" {
			t.Fatalf("path = %q", got)
		}
		_, _ = w.Write([]byte(`{"value":[]}`))
	}))
	defer server.Close()

	client, err := New(staticToken("test-token"), WithBaseURL("https://graph.microsoft.com/v1.0"))
	if err != nil {
		t.Fatal(err)
	}
	var page Page[testMessage]
	if _, err := client.Get(context.Background(), server.URL+"/next", Params{}, &page); err != nil {
		t.Fatal(err)
	}
}

func TestClientAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("request-id", "req-1")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"ErrorAccessDenied","message":"denied"}}`))
	}))
	defer server.Close()

	client, err := New(staticToken("test-token"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_, err = client.Get(context.Background(), "/me", Params{}, &out)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want APIError", err)
	}
	if !errors.Is(err, ErrRequestFailed) {
		t.Fatalf("err = %v, want ErrRequestFailed", err)
	}
	if apiErr.StatusCode != http.StatusForbidden || apiErr.Code != "ErrorAccessDenied" || apiErr.RequestID != "req-1" {
		t.Fatalf("apiErr = %+v", apiErr)
	}
	if !IsForbidden(err) || IsUnauthorized(err) || IsNotFound(err) {
		t.Fatalf("status helpers returned unexpected values for %v", err)
	}
}

func TestClientAPIErrorUsesNestedInnerRequestID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"BadRequest","message":"bad","innerError":{"date":"now","innerError":{"request-id":"nested-req"}}}}`))
	}))
	defer server.Close()

	client, err := New(staticToken("test-token"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_, err = client.Get(context.Background(), "/me", Params{}, &out)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want APIError", err)
	}
	if apiErr.RequestID != "nested-req" {
		t.Fatalf("RequestID = %q, want nested-req", apiErr.RequestID)
	}
}

func TestClientRetriesRetryAfter(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"TooManyRequests","message":"slow down"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer server.Close()

	var slept []time.Duration
	client, err := New(
		staticToken("test-token"),
		WithBaseURL(server.URL),
		WithSleeper(func(_ context.Context, delay time.Duration) error {
			slept = append(slept, delay)
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	var out testMessage
	if _, err := client.Get(context.Background(), "/me", Params{}, &out); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if len(slept) != 1 || slept[0] != time.Second {
		t.Fatalf("slept = %v, want [1s]", slept)
	}
}

func TestClientDoesNotRetryUnsafeMethodByDefault(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"ServiceUnavailable","message":"try later"}}`))
	}))
	defer server.Close()

	client, err := New(
		staticToken("test-token"),
		WithBaseURL(server.URL),
		WithSleeper(func(context.Context, time.Duration) error {
			t.Fatal("unsafe POST should not sleep for a retry")
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Post(context.Background(), "/me/sendMail", Params{}, map[string]string{"subject": "hello"}, nil)
	if !IsStatus(err, http.StatusServiceUnavailable) {
		t.Fatalf("err = %v, want 503 APIError", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestClientRetriesUnsafeMethodWhenOptedIn(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client, err := New(
		staticToken("test-token"),
		WithBaseURL(server.URL),
		WithRetryUnsafeMethods(true),
		WithSleeper(func(context.Context, time.Duration) error { return nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Post(context.Background(), "/me/sendMail", Params{}, map[string]string{"subject": "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestClientRetriesHTTPDateRetryAfter(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", time.Now().Add(time.Second).UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer server.Close()

	var slept []time.Duration
	client, err := New(
		staticToken("test-token"),
		WithBaseURL(server.URL),
		WithSleeper(func(_ context.Context, delay time.Duration) error {
			slept = append(slept, delay)
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	var out testMessage
	if _, err := client.Get(context.Background(), "/me", Params{}, &out); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if len(slept) != 1 || slept[0] <= 0 {
		t.Fatalf("slept = %v, want positive delay", slept)
	}
}

func TestClientCapsRetryDelay(t *testing.T) {
	header := http.Header{"Retry-After": []string{"3600"}}
	if got := retryDelay(header, 0, 10*time.Second); got != 10*time.Second {
		t.Fatalf("retryDelay = %v, want 10s", got)
	}
}

func TestClientStreamsWriter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer server.Close()

	client, err := New(staticToken("test-token"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := client.Get(context.Background(), "/download", Params{}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "payload" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestDecodeResponseStreamsWriterBeforeReadError(t *testing.T) {
	body := &readErrorAfterFirstChunk{chunk: []byte("partial")}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       body,
	}
	var out bytes.Buffer
	_, err := decodeResponse(resp, &out)
	if err == nil {
		t.Fatal("expected read error")
	}
	if out.String() != "partial" {
		t.Fatalf("writer saw %q, want streamed partial chunk", out.String())
	}
}

type testMessage struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
}

func staticToken(value string) TokenSource {
	return TokenSourceFunc(func(context.Context) (string, error) {
		return value, nil
	})
}

type readErrorAfterFirstChunk struct {
	chunk []byte
	read  bool
}

func (r *readErrorAfterFirstChunk) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.ErrUnexpectedEOF
	}
	r.read = true
	return copy(p, r.chunk), nil
}

func (r *readErrorAfterFirstChunk) Close() error { return nil }

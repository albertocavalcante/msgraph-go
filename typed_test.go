package msgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTypedGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `W/"abc"`)
		_, _ = w.Write([]byte(`{"id":"1","subject":"hi"}`))
	}))
	defer server.Close()

	client := newTypedTestClient(t, server.URL)
	message, resp, err := Get[testMessage](context.Background(), client, "/me/messages/1", Params{})
	if err != nil {
		t.Fatal(err)
	}
	if message.ID != "1" || message.Subject != "hi" {
		t.Fatalf("message = %+v", message)
	}
	if got, want := resp.ETag, `W/"abc"`; got != want {
		t.Fatalf("ETag = %q, want %q", got, want)
	}
}

func TestTypedGetReturnsZeroValueOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"ErrorItemNotFound","message":"gone"}}`))
	}))
	defer server.Close()

	client := newTypedTestClient(t, server.URL)
	message, _, err := Get[testMessage](context.Background(), client, "/me/messages/1", Params{})
	if !IsNotFound(err) {
		t.Fatalf("err = %v, want 404", err)
	}
	if message != (testMessage{}) {
		t.Fatalf("message = %+v, want zero value", message)
	}
	if !HasCode(err, "erroritemnotfound") {
		t.Fatalf("HasCode did not match case-insensitively: %v", err)
	}
}

func TestTypedPostAndPatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(w, `{"id":"1","subject":%q}`, body["subject"])
	}))
	defer server.Close()

	client := newTypedTestClient(t, server.URL)
	ctx := context.Background()

	created, _, err := Post[testMessage](ctx, client, "/me/messages", Params{}, map[string]any{"subject": "new"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Subject != "new" {
		t.Fatalf("created = %+v", created)
	}

	patched, _, err := Patch[testMessage](ctx, client, "/me/messages/1", Params{}, map[string]any{"subject": "edited"})
	if err != nil {
		t.Fatal(err)
	}
	if patched.Subject != "edited" {
		t.Fatalf("patched = %+v", patched)
	}
}

func TestPostNoContentAndDelete(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client := newTypedTestClient(t, server.URL)
	ctx := context.Background()

	if _, err := PostNoContent(ctx, client, "/me/sendMail", Params{}, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Delete(ctx, client, "/me/messages/1", Params{}); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 2 || methods[0] != http.MethodPost || methods[1] != http.MethodDelete {
		t.Fatalf("methods = %v", methods)
	}
}

func TestCollectAndFirst(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"value":[{"id":"3"}]}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"value":[{"id":"1"},{"id":"2"}],"@odata.nextLink":%q}`, server.URL+"/me/messages?page=2")
	}))
	defer server.Close()

	client := newTypedTestClient(t, server.URL)
	ctx := context.Background()

	items, err := Collect[testMessage](ctx, client, "/me/messages", Params{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}

	first, found, err := First[testMessage](ctx, client, "/me/messages", Params{})
	if err != nil {
		t.Fatal(err)
	}
	if !found || first.ID != "1" {
		t.Fatalf("first = %+v, found = %v", first, found)
	}
}

func TestFirstReportsEmptyCollection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"value":[]}`))
	}))
	defer server.Close()

	client := newTypedTestClient(t, server.URL)
	value, found, err := First[testMessage](context.Background(), client, "/me/messages", Params{})
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("found = true for empty collection, value = %+v", value)
	}
}

func TestDeltaReturnsLinkFromFinalPage(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_, _ = fmt.Fprintf(w, `{"value":[{"id":"2"}],"@odata.deltaLink":%q}`, server.URL+"/delta?token=next")
			return
		}
		_, _ = fmt.Fprintf(w, `{"value":[{"id":"1"}],"@odata.nextLink":%q}`, server.URL+"/delta?page=2")
	}))
	defer server.Close()

	client := newTypedTestClient(t, server.URL)
	items, deltaLink, err := Delta[testMessage](context.Background(), client, "/delta", Params{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if want := server.URL + "/delta?token=next"; deltaLink != want {
		t.Fatalf("deltaLink = %q, want %q", deltaLink, want)
	}
}

func TestPageCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("$count"); got != "true" {
			t.Fatalf("$count = %q", got)
		}
		_, _ = w.Write([]byte(`{"value":[{"id":"1"}],"@odata.count":42}`))
	}))
	defer server.Close()

	client := newTypedTestClient(t, server.URL)
	page, _, err := Get[Page[testMessage]](context.Background(), client, "/me/messages", Params{Count: true})
	if err != nil {
		t.Fatal(err)
	}
	if page.Count == nil || *page.Count != 42 {
		t.Fatalf("Count = %v", page.Count)
	}
}

func TestConditionalRequestHeaders(t *testing.T) {
	var ifMatch, ifNoneMatch string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ifMatch = r.Header.Get("If-Match")
		ifNoneMatch = r.Header.Get("If-None-Match")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	client := newTypedTestClient(t, server.URL)
	resp, err := client.Do(context.Background(), Request{
		Method:      http.MethodGet,
		URL:         "/me/messages/1",
		IfMatch:     `W/"a"`,
		IfNoneMatch: `W/"b"`,
	}, nil)
	if !IsNotModified(err) {
		t.Fatalf("err = %v, want ErrNotModified", err)
	}
	if resp == nil || resp.StatusCode != http.StatusNotModified {
		t.Fatalf("resp = %+v", resp)
	}
	if ifMatch != `W/"a"` || ifNoneMatch != `W/"b"` {
		t.Fatalf("If-Match = %q, If-None-Match = %q", ifMatch, ifNoneMatch)
	}
}

func TestPreconditionFailedClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = w.Write([]byte(`{"error":{"code":"ErrorIrresolvableConflict","message":"stale"}}`))
	}))
	defer server.Close()

	client := newTypedTestClient(t, server.URL)
	_, err := client.Do(context.Background(), Request{
		Method:  http.MethodPatch,
		URL:     "/me/messages/1",
		IfMatch: `W/"stale"`,
		Body:    map[string]any{"isRead": true},
	}, nil)
	if !IsPreconditionFailed(err) {
		t.Fatalf("err = %v, want 412", err)
	}
}

func TestAPIErrorCarriesRetryAfterTargetAndDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{
			"code":"TooManyRequests",
			"message":"throttled",
			"target":"subject",
			"details":[{"code":"Detail1","message":"bad","target":"body"}]
		}}`))
	}))
	defer server.Close()

	// Retries are disabled so the 429 surfaces instead of being slept through.
	client, err := New(staticToken("test-token"), WithBaseURL(server.URL), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = Get[testMessage](context.Background(), client, "/me/messages/1", Params{})
	if !IsThrottled(err) {
		t.Fatalf("err = %v, want 429", err)
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.RetryAfter != 17*time.Second {
		t.Fatalf("RetryAfter = %v, want 17s", apiErr.RetryAfter)
	}
	if apiErr.Target != "subject" {
		t.Fatalf("Target = %q", apiErr.Target)
	}
	if len(apiErr.Details) != 1 || apiErr.Details[0].Code != "Detail1" {
		t.Fatalf("Details = %+v", apiErr.Details)
	}
	if !HasCode(err, "Detail1") {
		t.Fatal("HasCode did not search details")
	}
}

func newTypedTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := New(staticToken("test-token"), WithBaseURL(baseURL))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

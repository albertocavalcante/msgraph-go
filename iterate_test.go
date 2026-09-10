package msgraph

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestItemsPagesAcrossNextLink(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/v1.0/me/messages":
			if got := r.URL.Query().Get("$top"); got != "1" {
				t.Fatalf("first $top = %q", got)
			}
			_, _ = w.Write([]byte(`{"value":[{"id":"1","subject":"one"}],"@odata.nextLink":"` + "http://" + r.Host + `/v1.0/next"}`))
		case "/v1.0/next":
			if got := r.URL.Query().Get("$top"); got != "" {
				t.Fatalf("next link unexpectedly kept $top = %q", got)
			}
			_, _ = w.Write([]byte(`{"value":[{"id":"2","subject":"two"}]}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := New(staticToken("test-token"), WithBaseURL(server.URL+"/v1.0"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for msg, err := range Items[testMessage](context.Background(), client, "/me/messages", Params{Top: 1}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, msg.Subject)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("got = %v", got)
	}
}

func TestPagesDetectsRepeatedNextLink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"value":[],"@odata.nextLink":"` + "http://" + r.Host + `/v1.0/loop"}`))
	}))
	defer server.Close()

	client, err := New(staticToken("test-token"), WithBaseURL(server.URL+"/v1.0"))
	if err != nil {
		t.Fatal(err)
	}
	var gotErr error
	for _, err := range Pages[testMessage](context.Background(), client, "/loop", Params{}) {
		if err != nil {
			gotErr = err
			break
		}
	}
	if !errors.Is(gotErr, ErrPageCycle) {
		t.Fatalf("err = %v, want ErrPageCycle", gotErr)
	}
}

func TestPagesHonorsMaxPages(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"value":[],"@odata.nextLink":"` + "http://" + r.Host + `/v1.0/page-` + string(rune('0'+calls)) + `"}`))
	}))
	defer server.Close()

	client, err := New(staticToken("test-token"), WithBaseURL(server.URL+"/v1.0"))
	if err != nil {
		t.Fatal(err)
	}
	var gotErr error
	for _, err := range Pages[testMessage](context.Background(), client, "/start", Params{}, WithMaxPages(1)) {
		if err != nil {
			gotErr = err
			break
		}
	}
	if !errors.Is(gotErr, ErrMaxPagesExceeded) {
		t.Fatalf("err = %v, want ErrMaxPagesExceeded", gotErr)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

// A server that never repeats a nextLink is not a cycle, so cycle detection
// never fires. Without a page bound the traversal runs forever and the set of
// seen links grows with it.
func TestPagingStopsOnAnEndlessChain(t *testing.T) {
	var served int
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		_, _ = fmt.Fprintf(w, `{"value":[{"id":"%d"}],"@odata.nextLink":%q}`,
			served, fmt.Sprintf("%s/page/%d", base, served))
	}))
	defer server.Close()
	base = server.URL

	client, err := New(staticToken("t"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}

	var pages int
	var lastErr error
	for _, err := range Pages[testMessage](context.Background(), client, "/me/messages", Params{},
		WithMaxPages(25)) {
		if err != nil {
			lastErr = err
			break
		}
		pages++
	}
	if !errors.Is(lastErr, ErrMaxPagesExceeded) {
		t.Fatalf("err = %v, want ErrMaxPagesExceeded", lastErr)
	}
	if pages != 25 {
		t.Fatalf("pages = %d, want 25", pages)
	}
}

// The default bound has to exist, or an endless chain is unbounded by default.
func TestDefaultPageBoundIsSet(t *testing.T) {
	if DefaultMaxPages <= 0 {
		t.Fatal("DefaultMaxPages must be positive")
	}
	// And it must be far above any legitimate traversal: the largest mailbox
	// tested here is 231,000 messages, which is 462 pages at the page size
	// Graph actually serves.
	const largestRealisticTraversal = 462
	if DefaultMaxPages < largestRealisticTraversal*10 {
		t.Fatalf("DefaultMaxPages = %d, too close to real traversals", DefaultMaxPages)
	}
}

// Opting out is still possible for a trusted server.
func TestMaxPagesCanBeDisabled(t *testing.T) {
	var served int
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		if served >= 3 {
			_, _ = w.Write([]byte(`{"value":[]}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"value":[{"id":"%d"}],"@odata.nextLink":%q}`,
			served, fmt.Sprintf("%s/page/%d", base, served))
	}))
	defer server.Close()
	base = server.URL

	client, err := New(staticToken("t"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	items, err := Collect[testMessage](context.Background(), client, "/me/messages", Params{},
		WithMaxPages(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
}

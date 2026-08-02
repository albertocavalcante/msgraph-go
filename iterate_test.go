package msgraph

import (
	"context"
	"errors"
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

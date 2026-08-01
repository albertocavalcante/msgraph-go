package msgraph

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/$batch" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var envelope batchEnvelope
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		if len(envelope.Requests) != 2 {
			t.Fatalf("requests = %d, want 2", len(envelope.Requests))
		}
		if envelope.Requests[1].URL != "/me/messages?$top=1" {
			t.Fatalf("absolute URL normalized to %q", envelope.Requests[1].URL)
		}
		_, _ = w.Write([]byte(`{"responses":[{"id":"1","status":200,"body":{"id":"me"}},{"id":"2","status":404,"body":{"error":{"code":"ErrorItemNotFound"}}}]}`))
	}))
	defer server.Close()

	client, err := New(staticToken("test-token"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	responses, err := client.Batch(context.Background(), []BatchRequest{
		{ID: "1", Method: http.MethodGet, URL: "/me"},
		{ID: "2", Method: http.MethodGet, URL: server.URL + "/me/messages?$top=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(responses) != 2 {
		t.Fatalf("responses = %d, want 2", len(responses))
	}
	if responses[1].Status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", responses[1].Status)
	}
}

func TestBatchValidatesSize(t *testing.T) {
	client, err := New(staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	requests := make([]BatchRequest, maxBatchRequests+1)
	for i := range requests {
		requests[i] = BatchRequest{ID: "id", Method: http.MethodGet, URL: "/me"}
	}
	_, err = client.Batch(context.Background(), requests)
	if !errors.Is(err, errBatchTooLarge) {
		t.Fatalf("err = %v, want errBatchTooLarge", err)
	}
}

package msgraph

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTranslateExchangeIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1.0/me/translateExchangeIds" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var req TranslateExchangeIDsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if !equalStrings(req.InputIDs, []string{"ews-1"}) {
			t.Fatalf("InputIDs = %v", req.InputIDs)
		}
		if req.SourceIDType != ExchangeIDFormatEWSID || req.TargetIDType != ExchangeIDFormatRESTImmutableEntryID {
			t.Fatalf("formats = %q -> %q", req.SourceIDType, req.TargetIDType)
		}
		_, _ = w.Write([]byte(`{"value":[{"sourceId":"ews-1","targetId":"immutable-1"}]}`))
	}))
	defer server.Close()

	client, err := New(staticToken("test-token"), WithBaseURL(server.URL+"/v1.0"))
	if err != nil {
		t.Fatal(err)
	}
	got, resp, err := client.TranslateExchangeIDs(
		context.Background(),
		"me",
		[]string{"ews-1"},
		ExchangeIDFormatEWSID,
		ExchangeIDFormatRESTImmutableEntryID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d", resp.StatusCode)
	}
	if len(got) != 1 || got[0].SourceID != "ews-1" || got[0].TargetID != "immutable-1" {
		t.Fatalf("got %+v", got)
	}
}

func TestTranslateExchangeIDsUserPathEscapesIdentifier(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/v1.0/users/a%2Fb@example.com/translateExchangeIds" {
			t.Fatalf("escaped path = %q", r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte(`{"value":[]}`))
	}))
	defer server.Close()

	client, err := New(staticToken("test-token"), WithBaseURL(server.URL+"/v1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.TranslateExchangeIDs(
		context.Background(),
		"a/b@example.com",
		[]string{"id"},
		ExchangeIDFormatRESTID,
		ExchangeIDFormatEWSID,
	); err != nil {
		t.Fatal(err)
	}
}

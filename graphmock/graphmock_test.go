package graphmock_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	msgraph "github.com/albertocavalcante/msgraph-go"
	"github.com/albertocavalcante/msgraph-go/graphmock"
)

type message struct {
	ID       string    `json:"id,omitempty"`
	Subject  string    `json:"subject,omitempty"`
	IsRead   bool      `json:"isRead,omitempty"`
	Received time.Time `json:"receivedDateTime,omitempty"`
}

// Responses are built from typed values, so a change to the model shows up
// here as a compile error rather than as a JSON literal that still parses.
func TestTypedResponses(t *testing.T) {
	server := graphmock.New(t)
	server.Handle("GET /me/messages/{id}", func(r *graphmock.Request) graphmock.Response {
		return graphmock.Item(message{ID: r.PathValue("id"), Subject: "hello"})
	})

	client := server.Client()
	got, _, err := msgraph.Get[message](context.Background(), client, "/me/messages/abc", msgraph.Params{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "abc" || got.Subject != "hello" {
		t.Fatalf("message = %+v", got)
	}
}

func TestCollectionAndPaging(t *testing.T) {
	server := graphmock.New(t)
	server.Handle("GET /me/messages", func(r *graphmock.Request) graphmock.Response {
		if r.Query.Has("page") {
			return graphmock.Collection([]message{{ID: "3"}}, "")
		}
		return graphmock.Collection([]message{{ID: "1"}, {ID: "2"}}, server.Link("/me/messages", "page=2"))
	})

	items, err := msgraph.Collect[message](context.Background(), server.Client(), "/me/messages", msgraph.Params{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	if got := len(server.Requests()); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
}

// The constraint suite is the point of the package: a filter Exchange refuses
// must be refused here too. Each of these was a production-only failure that
// every string-literal test happily passed.
func TestExchangeMailConstraints(t *testing.T) {
	tests := []struct {
		name     string
		params   msgraph.Params
		wantCode string
	}{
		{
			name: "endswith is not implemented for mail",
			params: msgraph.Params{
				Where: msgraph.EndsWith("from/emailAddress/address", "@example.com"),
			},
			wantCode: "ErrorInvalidUrlQueryFilter",
		},
		{
			name: "recipient lambdas are refused",
			params: msgraph.Params{
				Where: msgraph.Any("toRecipients", "r", msgraph.Eq("r/emailAddress/address", "a@b.com")),
			},
			wantCode: "ErrorInvalidUrlQueryFilter",
		},
		{
			name: "an unindexed filter cannot be sorted",
			params: msgraph.Params{
				Where: msgraph.Eq("hasAttachments", true),
				Sort:  []msgraph.Order{msgraph.Desc("receivedDateTime")},
			},
			wantCode: "InefficientFilter",
		},
		{
			name: "subject search cannot be sorted",
			params: msgraph.Params{
				Where: msgraph.Contains("subject", "invoice"),
				Sort:  []msgraph.Order{msgraph.Desc("receivedDateTime")},
			},
			wantCode: "InefficientFilter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := graphmock.New(t, graphmock.WithConstraints(graphmock.ExchangeMail()...))
			server.Handle("GET /me/messages", func(*graphmock.Request) graphmock.Response {
				return graphmock.Collection([]message{{ID: "1"}}, "")
			})

			_, _, err := msgraph.Get[msgraph.Page[message]](context.Background(),
				server.Client(), "/me/messages", tt.params)
			if err == nil {
				t.Fatal("the service would have refused this request, the mock did not")
			}
			if !msgraph.HasCode(err, tt.wantCode) {
				t.Fatalf("err = %v, want code %s", err, tt.wantCode)
			}
		})
	}
}

// The same filters without a sort are accepted, which is what makes the
// constraint a real rule rather than a blanket ban.
func TestUnsortedFiltersAreAccepted(t *testing.T) {
	server := graphmock.New(t, graphmock.WithConstraints(graphmock.ExchangeMail()...))
	server.Handle("GET /me/messages", func(*graphmock.Request) graphmock.Response {
		return graphmock.Collection([]message{{ID: "1"}}, "")
	})

	for _, where := range []msgraph.Expr{
		msgraph.Eq("hasAttachments", true),
		msgraph.Contains("subject", "invoice"),
		msgraph.Eq("importance", "high"),
	} {
		if _, _, err := msgraph.Get[msgraph.Page[message]](context.Background(),
			server.Client(), "/me/messages", msgraph.Params{Where: where}); err != nil {
			t.Fatalf("unsorted filter %s was refused: %v", where.ODataFilter(), err)
		}
	}
}

// Sortable properties keep their ordering, including a category lambda, which
// a text-scanning check got wrong.
func TestSortableFiltersKeepTheirOrdering(t *testing.T) {
	server := graphmock.New(t, graphmock.WithConstraints(graphmock.ExchangeMail()...))
	server.Handle("GET /me/messages", func(*graphmock.Request) graphmock.Response {
		return graphmock.Collection([]message{{ID: "1"}}, "")
	})

	sorted := []msgraph.Order{msgraph.Desc("receivedDateTime")}
	for _, where := range []msgraph.Expr{
		msgraph.Eq("isRead", false),
		msgraph.And(msgraph.Eq("isRead", false), msgraph.Ge("receivedDateTime", time.Now())),
		msgraph.Any("categories", "c", msgraph.Eq("c", "Red Team")),
	} {
		if _, _, err := msgraph.Get[msgraph.Page[message]](context.Background(),
			server.Client(), "/me/messages", msgraph.Params{Where: where, Sort: sorted}); err != nil {
			t.Fatalf("sortable filter %s was refused: %v", where.ODataFilter(), err)
		}
	}
}

// Graph pages delta at ten by default and caps a request at about 512. A
// client that does not ask pays for it in requests.
func TestDeltaPageSizeBehaviour(t *testing.T) {
	tests := []struct {
		name     string
		prefer   []msgraph.PreferDirective
		wantSize int
	}{
		{"no request gets the small default", nil, graphmock.ExchangeMailDeltaDefaultPageSize},
		{"a sensible request is honored", []msgraph.PreferDirective{msgraph.MaxPageSizePreference(500)}, 500},
		{"an oversized request is capped", []msgraph.PreferDirective{msgraph.MaxPageSizePreference(5000)}, graphmock.ExchangeMailPageCap},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var served int
			server := graphmock.New(t, graphmock.WithConstraints(graphmock.CapPageSize(graphmock.ExchangeMailPageCap)))
			server.Handle("GET /me/mailFolders/{folder}/messages/delta", func(r *graphmock.Request) graphmock.Response {
				served = r.EffectivePageSize(graphmock.ExchangeMailDeltaDefaultPageSize, graphmock.ExchangeMailPageCap)
				return graphmock.Delta([]message{}, server.Link("/delta", "token=done"))
			})

			_, _, err := msgraph.Do[msgraph.Page[message]](context.Background(), server.Client(), msgraph.Request{
				Method: http.MethodGet,
				URL:    "/me/mailFolders/inbox/messages/delta",
				Prefer: tt.prefer,
			})
			if err != nil {
				t.Fatal(err)
			}
			if served != tt.wantSize {
				t.Fatalf("served page size = %d, want %d", served, tt.wantSize)
			}
		})
	}
}

// A client that forgets the bearer token should fail loudly in tests.
func TestRequireAuthorization(t *testing.T) {
	server := graphmock.New(t, graphmock.WithConstraints(graphmock.RequireAuthorization()))
	server.Handle("GET /me", func(*graphmock.Request) graphmock.Response {
		return graphmock.Item(message{ID: "1"})
	})

	// The real client always sends one.
	if _, _, err := msgraph.Get[message](context.Background(), server.Client(), "/me", msgraph.Params{}); err != nil {
		t.Fatalf("authenticated request refused: %v", err)
	}

	// A bare HTTP request does not.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL()+"/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestFaultsAndRetries(t *testing.T) {
	var attempts int
	server := graphmock.New(t)
	server.Handle("GET /me/messages", func(*graphmock.Request) graphmock.Response {
		attempts++
		if attempts < 3 {
			return graphmock.Throttled(0)
		}
		return graphmock.Collection([]message{{ID: "1"}}, "")
	})

	client := server.Client(
		msgraph.WithMaxRetries(5),
		msgraph.WithSleeper(func(context.Context, time.Duration) error { return nil }),
	)
	items, err := msgraph.Collect[message](context.Background(), client, "/me/messages", msgraph.Params{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || attempts != 3 {
		t.Fatalf("items = %d after %d attempts", len(items), attempts)
	}
}

func TestFaultSurfacesAsAPIError(t *testing.T) {
	server := graphmock.New(t)
	server.Handle("GET /me/messages/{id}", func(*graphmock.Request) graphmock.Response {
		return graphmock.Fault(http.StatusNotFound, "ErrorItemNotFound", "gone")
	})

	_, _, err := msgraph.Get[message](context.Background(), server.Client(), "/me/messages/x", msgraph.Params{})
	var apiErr *msgraph.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Code != "ErrorItemNotFound" || apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("apiErr = %+v", apiErr)
	}
}

// Request bodies decode into typed values too, so an assertion about what was
// sent is checked against the model rather than against a substring.
func TestDecodeRequestBody(t *testing.T) {
	type sendMail struct {
		Message         message `json:"message"`
		SaveToSentItems bool    `json:"saveToSentItems"`
	}

	var got sendMail
	server := graphmock.New(t)
	server.Handle("POST /me/sendMail", func(r *graphmock.Request) graphmock.Response {
		got = graphmock.Decode[sendMail](t, r)
		return graphmock.Empty(http.StatusAccepted)
	})

	_, err := msgraph.PostNoContent(context.Background(), server.Client(), "/me/sendMail", msgraph.Params{},
		sendMail{Message: message{Subject: "hi"}, SaveToSentItems: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Message.Subject != "hi" || !got.SaveToSentItems {
		t.Fatalf("received %+v", got)
	}
}

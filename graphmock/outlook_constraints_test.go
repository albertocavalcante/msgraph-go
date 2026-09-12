package graphmock_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/albertocavalcante/msgraph-go/graphmock"
)

// post sends a body to the fake and reports the status, so a constraint can be
// checked without a typed client in the way.
func post(t *testing.T, server *graphmock.Server, method, path, body string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method,
		server.URL()+path, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestRequireRuleSequence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		// The service will not choose a position, so a rule without one is
		// refused. Every rule built from a zero value looks like this.
		{"missing", `{"displayName":"x"}`, http.StatusBadRequest},
		{"zero", `{"displayName":"x","sequence":0}`, http.StatusBadRequest},
		{"negative", `{"displayName":"x","sequence":-1}`, http.StatusBadRequest},
		{"positive", `{"displayName":"x","sequence":1}`, http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := graphmock.New(t, graphmock.WithConstraints(graphmock.RequireRuleSequence()))
			server.Handle("POST /me/mailFolders/{folder}/messageRules",
				func(*graphmock.Request) graphmock.Response {
					return graphmock.Created(map[string]any{"id": "r1"})
				})
			if got := post(t, server, http.MethodPost, "/me/mailFolders/inbox/messageRules", tc.body); got != tc.want {
				t.Errorf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRequireAllDayAtMidnight(t *testing.T) {
	t.Parallel()
	const midnight = `{"isAllDay":true,
		"start":{"dateTime":"2026-09-22T00:00:00.0000000","timeZone":"UTC"},
		"end":{"dateTime":"2026-09-23T00:00:00.0000000","timeZone":"UTC"}}`
	// What a client reading a bare date in a zone west of UTC produces.
	const shifted = `{"isAllDay":true,
		"start":{"dateTime":"2026-09-22T07:00:00.0000000","timeZone":"UTC"},
		"end":{"dateTime":"2026-09-23T07:00:00.0000000","timeZone":"UTC"}}`
	const timed = `{"isAllDay":false,
		"start":{"dateTime":"2026-09-22T09:00:00.0000000","timeZone":"UTC"},
		"end":{"dateTime":"2026-09-22T10:00:00.0000000","timeZone":"UTC"}}`

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"all-day at midnight", midnight, http.StatusCreated},
		{"all-day shifted by a zone", shifted, http.StatusBadRequest},
		// A timed event is unaffected: the rule is only about all-day ones.
		{"timed event at any hour", timed, http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := graphmock.New(t, graphmock.WithConstraints(graphmock.RequireAllDayAtMidnight()))
			server.Handle("POST /me/calendar/events", func(*graphmock.Request) graphmock.Response {
				return graphmock.Created(map[string]any{"id": "e1"})
			})
			if got := post(t, server, http.MethodPost, "/me/calendar/events", tc.body); got != tc.want {
				t.Errorf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

// Exchange answers a stale mail tag with 400 and ErrorInvalidChangeKey rather
// than the 412 the If-Match header implies, so a client checking for a
// precondition failure misses it entirely.
func TestRejectStaleETagUsesTheStatusTheServiceUses(t *testing.T) {
	t.Parallel()
	const current = `W/"CURRENT"`

	for _, tc := range []struct {
		name  string
		match string
		want  int
	}{
		{"no condition", "", http.StatusOK},
		{"wildcard", "*", http.StatusOK},
		{"current", current, http.StatusOK},
		{"stale", `W/"OLD"`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := graphmock.New(t, graphmock.WithConstraints(graphmock.RejectStaleETag(current)))
			server.Handle("PATCH /me/messages/{id}", func(*graphmock.Request) graphmock.Response {
				return graphmock.Item(map[string]any{"id": "m1"})
			})

			req, err := http.NewRequestWithContext(context.Background(), http.MethodPatch,
				server.URL()+"/me/messages/m1", bytes.NewReader([]byte(`{"isRead":true}`)))
			if err != nil {
				t.Fatal(err)
			}
			if tc.match != "" {
				req.Header.Set("If-Match", tc.match)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			// 412 would be the intuitive answer and is not the one to expect.
			if resp.StatusCode == http.StatusPreconditionFailed {
				t.Error("answered 412; Exchange answers 400 with ErrorInvalidChangeKey")
			}
		})
	}
}

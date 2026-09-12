package graphmock_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/albertocavalcante/msgraph-go/graphmock"
)

// get fetches a path and returns the body and headers, so a shaper can be
// checked on the wire rather than through a typed client.
func get(t *testing.T, server *graphmock.Server, path string) (string, http.Header) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		server.URL()+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body), resp.Header
}

// Graph names zones with Windows identifiers once a request asks for one. None
// is an IANA name, so none resolves -- and nothing is refused, which is why
// this is a shaper and why the failure is a calendar that renders confidently
// on the wrong date.
func TestNameZonesTheWindowsWay(t *testing.T) {
	t.Parallel()
	server := graphmock.New(t,
		graphmock.WithShapers(graphmock.NameZonesTheWindowsWay("Pacific Standard Time")))
	server.Handle("GET /me/calendar/events", func(*graphmock.Request) graphmock.Response {
		return graphmock.Raw(http.StatusOK, "application/json", []byte(`{"value":[{
			"id":"e1",
			"start":{"dateTime":"2026-10-12T00:00:00.0000000","timeZone":"UTC"},
			"end":{"dateTime":"2026-10-13T00:00:00.0000000","timeZone":"UTC"}
		}]}`))
	})

	body, _ := get(t, server, "/me/calendar/events")

	var payload struct {
		Value []struct {
			Start struct {
				DateTime string `json:"dateTime"`
				TimeZone string `json:"timeZone"`
			} `json:"start"`
			End struct {
				TimeZone string `json:"timeZone"`
			} `json:"end"`
		} `json:"value"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("shaper produced invalid JSON: %v\n%s", err, body)
	}
	if len(payload.Value) != 1 {
		t.Fatalf("lost the events: %s", body)
	}
	// Both ends, not just the first: a client reading one and assuming the
	// other is the same kind of mistake this reproduces.
	if got := payload.Value[0].Start.TimeZone; got != "Pacific Standard Time" {
		t.Errorf("start zone = %q", got)
	}
	if got := payload.Value[0].End.TimeZone; got != "Pacific Standard Time" {
		t.Errorf("end zone = %q", got)
	}
	// The clock must be untouched: only the zone naming changes.
	if got := payload.Value[0].Start.DateTime; got != "2026-10-12T00:00:00.0000000" {
		t.Errorf("the shaper altered the reading: %q", got)
	}
}

// A body with no zone in it must come back untouched, or the shaper would be
// rewriting responses it has no business in.
func TestZoneShaperLeavesOtherBodiesAlone(t *testing.T) {
	t.Parallel()
	const original = `{"value":[{"id":"m1","subject":"no zones here"}]}`
	server := graphmock.New(t, graphmock.WithShapers(graphmock.NameZonesTheWindowsWay("")))
	server.Handle("GET /me/messages", func(*graphmock.Request) graphmock.Response {
		return graphmock.Raw(http.StatusOK, "application/json", []byte(original))
	})

	body, _ := get(t, server, "/me/messages")
	var got, want any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(original), &want); err != nil {
		t.Fatal(err)
	}
	if string(mustMarshal(t, got)) != string(mustMarshal(t, want)) {
		t.Errorf("body changed:\n got %s\nwant %s", body, original)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Mail carries no ETag header. A client reading one finds nothing and turns
// every conditional write into an unconditional one.
func TestStripHeader(t *testing.T) {
	t.Parallel()
	server := graphmock.New(t, graphmock.WithShapers(graphmock.StripHeader("ETag")))
	server.Handle("GET /me/messages/{id}", func(*graphmock.Request) graphmock.Response {
		response := graphmock.Raw(http.StatusOK, "application/json",
			[]byte(`{"id":"m1","@odata.etag":"W/\"TAG\""}`))
		response.Header = http.Header{"ETag": {`W/"TAG"`}}
		return response
	})

	body, header := get(t, server, "/me/messages/m1")
	if got := header.Get("ETag"); got != "" {
		t.Errorf("ETag header survived: %q", got)
	}
	// The body property is where the version actually lives.
	if !contains(body, "@odata.etag") {
		t.Errorf("lost the body etag: %s", body)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// An upload session answers an intermediate chunk with 200 and the ranges it
// still wants, not the 202 the name suggests. Reading the status as completion
// truncates the attachment at the first chunk.
func TestOutlookChunkResponses(t *testing.T) {
	t.Parallel()
	server := graphmock.New(t)
	server.Handle("PUT /upload/session", func(r *graphmock.Request) graphmock.Response {
		if r.Header.Get("X-Final") == "yes" {
			return graphmock.OutlookChunkComplete(map[string]any{"id": "att-1"})
		}
		return graphmock.OutlookChunkAccepted(1024)
	})

	for _, tc := range []struct {
		name      string
		final     bool
		want      int
		hasRanges bool
	}{
		{"intermediate chunk", false, http.StatusOK, true},
		{"final chunk", true, http.StatusCreated, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPut,
				server.URL()+"/upload/session", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.final {
				req.Header.Set("X-Final", "yes")
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}

			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			// 202 is the intuitive answer for "keep going" and is not the one
			// the service gives.
			if resp.StatusCode == http.StatusAccepted {
				t.Error("answered 202; an Outlook session answers 200 with nextExpectedRanges")
			}
			if got := contains(string(body), "nextExpectedRanges"); got != tc.hasRanges {
				t.Errorf("nextExpectedRanges present = %v, want %v (body %s)", got, tc.hasRanges, body)
			}
		})
	}
}

package msgraph

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func noSleep(context.Context, time.Duration) error { return nil }

func TestTraceSimpleGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"1"}`))
	}))
	defer server.Close()
	client, err := New(staticToken("t"), WithBaseURL(server.URL+"/v1.0"))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	ctx, root := rec.root(t)

	if _, err := client.Get(ctx, "/me/messages/AAMkMessage", Params{}, &testMessage{}); err != nil {
		t.Fatal(err)
	}

	want := "msgraph.request\n  msgraph.token\n  GET /v1.0/me/messages/{id}\n"
	if got := rec.tree(root); got != want {
		t.Fatalf("span tree:\n%s\nwant:\n%s", got, want)
	}
	call := rec.children(root)[0]
	if call.scope != instrumentationName {
		t.Errorf("scope = %q, want %q", call.scope, instrumentationName)
	}
	call.wantAttr(t, attrMethod, "GET")
	call.wantAttr(t, attrURLTemplate, "/v1.0/me/messages/{id}")
	call.wantAttr(t, attrStatus, int64(200))
	if call.status != codes.Unset {
		t.Errorf("call status = %v, want unset", call.status)
	}

	attempt := rec.children(call)[1]
	if attempt.kind != trace.SpanKindClient {
		t.Errorf("attempt kind = %v, want client", attempt.kind)
	}
	host, port := hostPort(t, server.URL)
	attempt.wantAttr(t, attrMethod, "GET")
	attempt.wantAttr(t, attrServerAddr, host)
	attempt.wantAttr(t, attrServerPort, port)
	attempt.wantAttr(t, attrURLTemplate, "/v1.0/me/messages/{id}")
	attempt.wantAttr(t, attrStatus, int64(200))
	attempt.wantNoAttr(t, attrResendCount)
	attempt.wantAttr(t, attrConnReused, false)
	for _, key := range []string{"msgraph.http.connect_ms", "msgraph.http.first_byte_ms"} {
		if _, ok := attempt.attr(attribute.Key(key)); !ok {
			t.Errorf("attempt is missing %s", key)
		}
	}
	for _, s := range rec.spans {
		if !s.ended && s != root {
			t.Errorf("span %q was not ended", s.name)
		}
	}
}

func TestTraceRetriedRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"value":[]}`))
	}))
	defer server.Close()
	var slept []time.Duration
	client, err := New(staticToken("t"), WithBaseURL(server.URL), WithSleeper(func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	ctx, root := rec.root(t)

	if _, err := client.Get(ctx, "/me/messages", Params{}, &Page[testMessage]{}); err != nil {
		t.Fatal(err)
	}

	want := "msgraph.request\n  msgraph.token\n  GET /me/messages\n  msgraph.token\n  GET /me/messages\n"
	if got := rec.tree(root); got != want {
		t.Fatalf("span tree:\n%s\nwant:\n%s", got, want)
	}
	call := rec.children(root)[0]
	children := rec.children(call)
	first, second := children[1], children[3]
	first.wantAttr(t, attrStatus, int64(429))
	first.wantAttr(t, attrErrorType, "429")
	first.wantNoAttr(t, attrResendCount)
	second.wantAttr(t, attrStatus, int64(200))
	second.wantAttr(t, attrResendCount, int64(1))
	if second.status != codes.Unset {
		t.Errorf("second attempt status = %v, want unset", second.status)
	}
	call.wantAttr(t, attrStatus, int64(200))
	call.wantNoAttr(t, attrErrorType)

	if len(call.events) != 1 || call.events[0].name != eventRetry {
		t.Fatalf("call events = %+v, want one %s", call.events, eventRetry)
	}
	event := call.events[0].attrs
	wantEvent := map[string]any{
		"msgraph.retry.reason":      "throttled",
		"msgraph.retry.retry_after": true,
		"msgraph.retry.capped":      false,
		"msgraph.retry.delay_ms":    2000.0,
		"http.request.resend_count": int64(1),
		"http.response.status_code": int64(429),
	}
	for key, want := range wantEvent {
		got, ok := lookup(event, attribute.Key(key))
		if !ok || got.AsInterface() != want {
			t.Errorf("retry event %s = %v, want %v", key, got.AsInterface(), want)
		}
	}
	if len(slept) != 1 || slept[0] != 2*time.Second {
		t.Errorf("slept = %v, want [2s]", slept)
	}
}

func TestTraceFailedRequest(t *testing.T) {
	t.Run("graph error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"ErrorItemNotFound","message":"not found"}}`))
		}))
		defer server.Close()
		client, err := New(staticToken("t"), WithBaseURL(server.URL))
		if err != nil {
			t.Fatal(err)
		}
		rec := &recorder{}
		ctx, root := rec.root(t)

		if _, err := client.Get(ctx, "/me/messages/AAMk", Params{}, nil); !IsNotFound(err) {
			t.Fatalf("err = %v, want 404", err)
		}

		call := rec.children(root)[0]
		call.wantAttr(t, attrStatus, int64(404))
		call.wantAttr(t, attrErrorType, "404")
		call.wantAttr(t, attrGraphErrorCode, "ErrorItemNotFound")
		if call.status != codes.Error {
			t.Errorf("call status = %v, want error", call.status)
		}
		attempt := rec.children(call)[1]
		attempt.wantAttr(t, attrErrorType, "404")
		if attempt.status != codes.Error {
			t.Errorf("attempt status = %v, want error", attempt.status)
		}
	})

	t.Run("transport error", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		base := server.URL
		server.Close()
		client, err := New(staticToken("t"), WithBaseURL(base), WithMaxRetries(0))
		if err != nil {
			t.Fatal(err)
		}
		rec := &recorder{}
		ctx, root := rec.root(t)

		if _, err := client.Get(ctx, "/me", Params{}, nil); err == nil {
			t.Fatal("request to a closed server succeeded")
		}

		call := rec.children(root)[0]
		call.wantAttr(t, attrErrorType, "transport")
		call.wantNoAttr(t, attrStatus)
		attempt := rec.children(call)[1]
		attempt.wantAttr(t, attrErrorType, "transport")
		attempt.wantNoAttr(t, attrStatus)
		if !attempt.ended {
			t.Error("failed attempt span was not ended")
		}
	})

	t.Run("token error", func(t *testing.T) {
		client, err := New(TokenSourceFunc(func(context.Context) (string, error) {
			return "", errors.New("no credentials")
		}), WithBaseURL("https://graph.example.com"))
		if err != nil {
			t.Fatal(err)
		}
		rec := &recorder{}
		ctx, root := rec.root(t)

		_, err = client.Get(ctx, "/me", Params{}, nil)
		if err == nil || err.Error() != "token: no credentials" {
			t.Fatalf("err = %v, want the unchanged token error", err)
		}

		if got, want := rec.tree(root), "msgraph.request\n  msgraph.token\n"; got != want {
			t.Fatalf("span tree:\n%s\nwant:\n%s", got, want)
		}
		call := rec.children(root)[0]
		call.wantAttr(t, attrErrorType, "token")
		rec.children(call)[0].wantAttr(t, attrErrorType, "token")
	})
}

func TestTraceBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"responses":[{"id":"1","status":200},{"id":"2","status":404}]}`))
	}))
	defer server.Close()
	client, err := New(staticToken("t"), WithBaseURL(server.URL+"/v1.0"))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	ctx, root := rec.root(t)

	if _, err := client.Batch(ctx, []BatchRequest{
		{ID: "1", Method: "GET", URL: "/me"},
		{ID: "2", Method: "GET", URL: "/me/messages/AAMk"},
	}); err != nil {
		t.Fatal(err)
	}

	want := "msgraph.batch\n  msgraph.token\n  POST /v1.0/$batch\n"
	if got := rec.tree(root); got != want {
		t.Fatalf("span tree:\n%s\nwant:\n%s", got, want)
	}
	call := rec.children(root)[0]
	call.wantAttr(t, attrBatchSize, int64(2))
	call.wantAttr(t, attrBatchFailed, int64(1))
	call.wantAttr(t, attrMethod, "POST")
	call.wantAttr(t, attrStatus, int64(200))
}

func TestTraceDelta(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("$skiptoken") {
			_, _ = w.Write([]byte(`{"value":[{"id":"2"}],"@odata.deltaLink":"` + server.URL + `/v1.0/me/messages/delta?$deltatoken=next"}`))
			return
		}
		_, _ = w.Write([]byte(`{"value":[{"id":"1"}],"@odata.nextLink":"` + server.URL + `/v1.0/me/messages/delta?$skiptoken=page2"}`))
	}))
	defer server.Close()
	client, err := New(staticToken("t"), WithBaseURL(server.URL+"/v1.0"))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	ctx, root := rec.root(t)

	items, _, err := Delta[testMessage](ctx, client, "/me/messages/delta", Params{})
	if err != nil || len(items) != 2 {
		t.Fatalf("Delta = %d items, %v", len(items), err)
	}

	page := "  msgraph.request\n    msgraph.token\n    GET /v1.0/me/messages/delta\n"
	if got, want := rec.tree(root), "msgraph.delta\n"+page+page; got != want {
		t.Fatalf("span tree:\n%s\nwant:\n%s", got, want)
	}
	call := rec.children(root)[0]
	call.wantAttr(t, attrURLTemplate, "/v1.0/me/messages/delta")
	call.wantAttr(t, attrDeltaPages, int64(2))
	call.wantAttr(t, attrDeltaItems, int64(2))
}

func TestTraceDownload(t *testing.T) {
	client, err := New(staticToken("t"), WithHTTPClient(&http.Client{Transport: downloadRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "graph.microsoft.com" {
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://tenant.files.example/signed/file?tempauth=secret"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data"))}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	ctx, root := rec.root(t)

	var out bytes.Buffer
	if _, err := client.Download(ctx, "/me/drive/items/ITEM/content", Params{}, &out); err != nil {
		t.Fatal(err)
	}

	want := "msgraph.download\n  msgraph.token\n  GET /v1.0/me/drive/items/{id}/content\n  GET\n"
	if got := rec.tree(root); got != want {
		t.Fatalf("span tree:\n%s\nwant:\n%s", got, want)
	}
	content := rec.children(rec.children(root)[0])[2]
	content.wantAttr(t, attrContent, true)
	content.wantNoAttr(t, attrServerAddr)
	content.wantNoAttr(t, attrURLTemplate)
	if dump := rec.dump(); strings.Contains(dump, "tenant") || strings.Contains(dump, "secret") {
		t.Fatalf("content URL leaked into telemetry:\n%s", dump)
	}
}

// Every recognizable secret in a request, and in the error that comes back,
// must stay out of span names, attributes, events and log lines.
func TestTelemetryRedactsRequestData(t *testing.T) {
	const (
		token     = "sentinel-bearer-token"
		messageID = "AAMkSentinelMessageID"
		email     = "sentinel.user@example.com"
		filter    = "sentinel-filter-value"
		header    = "sentinel-header-value"
		body      = "sentinel-body-value"
	)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"ErrorItemNotFound","message":"` + email + ` has no ` + messageID + `"}}`))
	}))
	defer server.Close()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client, err := New(staticToken(token), WithBaseURL(server.URL+"/v1.0"), WithLogger(logger),
		WithSleeper(noSleep), WithRetryUnsafeMethods(true))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	ctx, _ := rec.root(t)

	_, err = client.Do(ctx, Request{
		Method: http.MethodPost,
		URL:    "/users/" + email + "/messages/" + messageID + "/forward",
		Params: Params{Filter: "subject eq '" + filter + "'"},
		Header: http.Header{"X-Sentinel": {header}},
		Body:   map[string]string{"comment": body},
	}, nil)
	if !IsNotFound(err) {
		t.Fatalf("err = %v, want 404", err)
	}

	recorded := rec.dump() + logs.String()
	if !strings.Contains(recorded, "/v1.0/users/{id}/messages/{id}/forward") {
		t.Fatalf("expected the redacted route in telemetry:\n%s", recorded)
	}
	if !strings.Contains(logs.String(), "msgraph: retrying request") {
		t.Fatalf("expected a retry log line:\n%s", logs.String())
	}
	for _, secret := range []string{token, messageID, email, url.PathEscape(email), filter, header, body} {
		if strings.Contains(recorded, secret) {
			t.Errorf("telemetry contains %q:\n%s", secret, recorded)
		}
	}
}

func TestRetryLogLine(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
	client, err := New(staticToken("t"), WithBaseURL(server.URL), WithLogger(logger),
		WithSleeper(noSleep), WithMaxRetryDelay(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.Get(context.Background(), "/me/events/AAMk", Params{}, nil); err != nil {
		t.Fatal(err)
	}

	want := `level=DEBUG msg="msgraph: retrying request" method=GET route=/me/events/{id} attempt=1 reason=throttled status=429 retry_after=true capped=true delay=1m0s` + "\n"
	if got := logs.String(); got != want {
		t.Fatalf("log =\n%s\nwant\n%s", got, want)
	}
}

// With an untraced context nothing is instrumented: the transport sees no span
// and no httptrace hooks, and the response is exactly what it was before.
func TestUntracedContextIsNotInstrumented(t *testing.T) {
	client, err := New(staticToken("t"), WithHTTPClient(&http.Client{Transport: downloadRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if trace.SpanFromContext(req.Context()).IsRecording() {
			t.Error("transport saw a recording span")
		}
		if httptrace.ContextClientTrace(req.Context()) != nil {
			t.Error("transport saw httptrace hooks")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"1"}`))}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	var out testMessage
	resp, err := client.Get(context.Background(), "/me", Params{}, &out)
	if err != nil || resp.StatusCode != http.StatusOK || out.ID != "1" {
		t.Fatalf("Get = %+v, %+v, %v", resp, out, err)
	}
}

// A caller's transport is wrapped, not replaced: it serves the request and
// sees the attempt span as its parent, so transport-level instrumentation
// nests beneath it.
func TestTraceWrapsCallerTransport(t *testing.T) {
	var seen trace.SpanContext
	client, err := New(staticToken("t"), WithHTTPClient(&http.Client{Transport: downloadRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = trace.SpanContextFromContext(req.Context())
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	ctx, root := rec.root(t)

	if _, err := client.Get(ctx, "/me", Params{}, nil); err != nil {
		t.Fatal(err)
	}

	attempt := rec.children(rec.children(root)[0])[1]
	if seen.SpanID() != attempt.sc.SpanID() {
		t.Fatalf("transport saw span %v, want the attempt span %v", seen.SpanID(), attempt.sc.SpanID())
	}
	attempt.wantAttr(t, attrStatus, int64(200))
}

func TestTraceConnectionReuse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"1"}`))
	}))
	defer server.Close()
	first, err := New(staticToken("t"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(staticToken("t"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	ctx, root := rec.root(t)

	// out is nil on purpose: a body nobody decodes must still be drained, or
	// the connection is discarded instead of returned to the pool.
	if _, err := first.Get(ctx, "/me", Params{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Get(ctx, "/me", Params{}, nil); err != nil {
		t.Fatal(err)
	}
	// A second client built by New shares the default transport's pool.
	if _, err := second.Get(ctx, "/me", Params{}, nil); err != nil {
		t.Fatal(err)
	}

	calls := rec.children(root)
	wantReused := []bool{false, true, true}
	for i, call := range calls {
		attempt := rec.children(call)[1]
		attempt.wantAttr(t, attrConnReused, wantReused[i])
		if _, dialed := attempt.attr(attrConnect); dialed == wantReused[i] {
			t.Errorf("request %d: connect timing present = %v, want %v", i, dialed, !wantReused[i])
		}
	}
}

func TestErrorTypeClassification(t *testing.T) {
	cases := map[string]error{
		"503":                &APIError{StatusCode: 503},
		"token":              &tokenFailure{cause: context.Canceled},
		"redirect_refused":   &requestFailure{cause: &url.Error{Err: &redirectFailure{cause: ErrUntrustedHost}}},
		"untrusted_host":     ErrUntrustedHost,
		"response_too_large": ErrResponseTooLarge,
		"page_cycle":         ErrPageCycle,
		"max_pages":          ErrMaxPagesExceeded,
		"canceled":           &requestFailure{cause: context.Canceled},
		"timeout":            &requestFailure{cause: context.DeadlineExceeded},
		"transport":          &requestFailure{cause: errors.New("connection reset")},
		"_OTHER":             errors.New("anything else"),
	}
	for want, err := range cases {
		if got := errorType(err); got != want {
			t.Errorf("errorType(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestSafeErrorCode(t *testing.T) {
	for code, want := range map[string]bool{
		"ErrorItemNotFound":          true,
		"Request_ResourceNotFound":   true,
		"":                           false,
		"user@example.com not found": false,
		strings.Repeat("A", 65):      false,
	} {
		if got := safeErrorCode(code); got != want {
			t.Errorf("safeErrorCode(%q) = %v, want %v", code, got, want)
		}
	}
}

func hostPort(t *testing.T, raw string) (string, int64) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	port, ok := originPort(u)
	if !ok {
		t.Fatalf("no port in %q", raw)
	}
	return u.Hostname(), int64(port)
}

// stallingBody stands for a response whose bytes stop arriving: any read
// would block, so the client must not read it.
type stallingBody struct {
	t      *testing.T
	closed bool
}

func (b *stallingBody) Read([]byte) (int, error) {
	b.t.Error("read a body that may never finish")
	return 0, io.EOF
}

func (b *stallingBody) Close() error {
	b.closed = true
	return nil
}

// Draining an unread body only pays off when it is small and its length is
// known; an unannounced or large body is closed without being read.
func TestUnreadBodyIsNotDrainedUnlessSmall(t *testing.T) {
	for _, length := range []int64{-1, maxDrainBytes + 1} {
		t.Run("get", func(t *testing.T) {
			body := &stallingBody{t: t}
			client, err := New(staticToken("t"), WithHTTPClient(&http.Client{Transport: downloadRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, ContentLength: length, Body: body}, nil
			})}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Get(context.Background(), "/me", Params{}, nil); err != nil {
				t.Fatal(err)
			}
			if !body.closed {
				t.Error("body was not closed")
			}
		})
		t.Run("download redirect", func(t *testing.T) {
			body := &stallingBody{t: t}
			client, err := New(staticToken("t"), WithHTTPClient(&http.Client{Transport: downloadRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "graph.microsoft.com" {
					return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://files.example/f"}}, ContentLength: length, Body: body}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data"))}, nil
			})}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Download(context.Background(), "/me/drive/items/ITEM/content", Params{}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if !body.closed {
				t.Error("redirect body was not closed")
			}
		})
	}
}

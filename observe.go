package msgraph

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// instrumentationName is the OpenTelemetry instrumentation scope.
const instrumentationName = "github.com/albertocavalcante/msgraph-go"

// Span names. They are part of the public telemetry contract documented in
// docs/observability.md.
const (
	spanRequest  = "msgraph.request"
	spanBatch    = "msgraph.batch"
	spanDelta    = "msgraph.delta"
	spanDownload = "msgraph.download"
	spanToken    = "msgraph.token"
	eventRetry   = "msgraph.retry"
)

// Attribute keys: OpenTelemetry HTTP semantic conventions where they apply,
// and a msgraph. prefix for the rest.
const (
	attrMethod      = attribute.Key("http.request.method")
	attrStatus      = attribute.Key("http.response.status_code")
	attrResendCount = attribute.Key("http.request.resend_count")
	attrServerAddr  = attribute.Key("server.address")
	attrServerPort  = attribute.Key("server.port")
	attrURLTemplate = attribute.Key("url.template")
	attrErrorType   = attribute.Key("error.type")

	attrGraphErrorCode = attribute.Key("msgraph.error.code")
	attrConnReused     = attribute.Key("msgraph.http.connection.reused")
	attrDNS            = attribute.Key("msgraph.http.dns_ms")
	attrConnect        = attribute.Key("msgraph.http.connect_ms")
	attrTLS            = attribute.Key("msgraph.http.tls_ms")
	attrFirstByte      = attribute.Key("msgraph.http.first_byte_ms")
	attrContent        = attribute.Key("msgraph.download.content")
	attrRetryReason    = attribute.Key("msgraph.retry.reason")
	attrRetryAfter     = attribute.Key("msgraph.retry.retry_after")
	attrRetryCapped    = attribute.Key("msgraph.retry.capped")
	attrRetryDelay     = attribute.Key("msgraph.retry.delay_ms")
	attrBatchSize      = attribute.Key("msgraph.batch.size")
	attrBatchFailed    = attribute.Key("msgraph.batch.failed")
	attrDeltaPages     = attribute.Key("msgraph.delta.pages")
	attrDeltaItems     = attribute.Key("msgraph.delta.items")
)

// Retry reasons recorded on msgraph.retry events and log lines.
const (
	retryThrottled   = "throttled"
	retryServerError = "server_error"
	retryTransport   = "transport_error"
)

// startSpan takes the tracer from the span already in ctx, so the library has
// no global state and no configuration: with an untraced context this is the
// API's built-in no-op.
func startSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return trace.SpanFromContext(ctx).TracerProvider().Tracer(instrumentationName).Start(ctx, name, opts...)
}

// endCall records the outcome of a logical client call and ends its span.
func endCall(span trace.Span, resp *Response, err error) {
	if span.IsRecording() {
		if resp != nil {
			span.SetAttributes(attrStatus.Int(resp.StatusCode))
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			span.SetAttributes(attrStatus.Int(apiErr.StatusCode))
			if safeErrorCode(apiErr.Code) {
				span.SetAttributes(attrGraphErrorCode.String(apiErr.Code))
			}
		}
		if err != nil && !errors.Is(err, ErrNotModified) {
			markError(span, errorType(err))
		}
	}
	span.End()
}

func markError(span trace.Span, kind string) {
	span.SetAttributes(attrErrorType.String(kind))
	span.SetStatus(codes.Error, kind)
}

// errorType classifies err into a fixed, low-cardinality value. Error strings
// are never recorded: Graph error messages can name users and resources.
func errorType(err error) string {
	var apiErr *APIError
	var tokenErr *tokenFailure
	var netErr net.Error
	var reqErr *requestFailure
	var urlErr *url.Error
	switch {
	case errors.As(err, &apiErr):
		return strconv.Itoa(apiErr.StatusCode)
	case errors.As(err, &tokenErr):
		return "token"
	case errors.Is(err, ErrRedirectRefused):
		return "redirect_refused"
	case errors.Is(err, ErrUntrustedHost):
		return "untrusted_host"
	case errors.Is(err, ErrResponseTooLarge):
		return "response_too_large"
	case errors.Is(err, ErrPageCycle):
		return "page_cycle"
	case errors.Is(err, ErrMaxPagesExceeded):
		return "max_pages"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case errors.As(err, &reqErr), errors.As(err, &urlErr):
		return "transport"
	default:
		return "_OTHER"
	}
}

// safeErrorCode admits Graph service codes such as "ErrorItemNotFound". The
// value comes from a response body, so anything that does not look like a
// code is dropped rather than recorded.
func safeErrorCode(code string) bool {
	if code == "" || len(code) > 64 {
		return false
	}
	for i := range len(code) {
		c := code[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

// tokenFailure marks an error from the TokenSource so it can be classified.
// Its message is unchanged from the plain "token: ..." wrapping it replaces.
type tokenFailure struct{ cause error }

func (e *tokenFailure) Error() string { return "token: " + e.cause.Error() }
func (e *tokenFailure) Unwrap() error { return e.cause }

func (c *Client) acquireToken(ctx context.Context) (string, error) {
	ctx, span := startSpan(ctx, spanToken)
	defer span.End()
	token, err := c.token.Token(ctx)
	if err != nil {
		markError(span, "token")
		return "", &tokenFailure{cause: err}
	}
	return token, nil
}

// roundTrip sends one HTTP attempt under its own client span. When graph is
// false the destination is outside Graph (a preauthenticated content URL), and
// neither its host nor its path is described: both can name a tenant or file.
//
// Instrumentation travels in the request context, so a caller's transport is
// used unchanged and sees the attempt span as its parent.
func roundTrip(client *http.Client, req *http.Request, attempt int, graph bool) (*http.Response, error) {
	ctx, span := startSpan(req.Context(), req.Method, trace.WithSpanKind(trace.SpanKindClient))
	if !span.IsRecording() {
		span.End()
		return client.Do(req)
	}
	span.SetAttributes(attrMethod.String(req.Method))
	if graph {
		// "{method} {url.template}", per the HTTP client span convention.
		route := routeTemplate(req.URL.EscapedPath())
		span.SetName(req.Method + " " + route)
		span.SetAttributes(attrURLTemplate.String(route), attrServerAddr.String(req.URL.Hostname()))
	} else {
		span.SetAttributes(attrContent.Bool(true))
	}
	if port, ok := originPort(req.URL); ok {
		span.SetAttributes(attrServerPort.Int64(int64(port)))
	}
	if attempt > 0 {
		span.SetAttributes(attrResendCount.Int(attempt))
	}
	timings := &connTimings{start: time.Now()}
	resp, err := client.Do(req.WithContext(httptrace.WithClientTrace(ctx, timings.clientTrace())))
	timings.annotate(span)
	if err != nil {
		markError(span, errorType(err))
		span.End()
		return nil, err
	}
	span.SetAttributes(attrStatus.Int(resp.StatusCode))
	if resp.StatusCode >= 400 {
		markError(span, strconv.Itoa(resp.StatusCode))
	}
	resp.Body = &spanBody{ReadCloser: resp.Body, span: span}
	return resp, nil
}

// spanBody ends the attempt span when the body is closed, so the span covers
// reading the response and not just its headers.
type spanBody struct {
	io.ReadCloser
	span trace.Span
	once sync.Once
}

func (b *spanBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { b.span.End() })
	return err
}

// connTimings collects net/http/httptrace callbacks, which the transport may
// invoke from its own goroutines, such as a parallel dial.
type connTimings struct {
	mu                        sync.Mutex
	start                     time.Time
	dnsStart, dnsDone         time.Time
	connectStart, connectDone time.Time
	tlsStart, tlsDone         time.Time
	firstByte                 time.Time
	gotConn, reused           bool
}

func (t *connTimings) clientTrace() *httptrace.ClientTrace {
	mark := func(field *time.Time) {
		t.mu.Lock()
		defer t.mu.Unlock()
		// Keep the first occurrence: a redirect within the attempt fires every
		// callback again for the next hop.
		if field.IsZero() {
			*field = time.Now()
		}
	}
	return &httptrace.ClientTrace{
		DNSStart:     func(httptrace.DNSStartInfo) { mark(&t.dnsStart) },
		DNSDone:      func(httptrace.DNSDoneInfo) { mark(&t.dnsDone) },
		ConnectStart: func(string, string) { mark(&t.connectStart) },
		ConnectDone: func(_, _ string, err error) {
			if err == nil {
				mark(&t.connectDone)
			}
		},
		TLSHandshakeStart: func() { mark(&t.tlsStart) },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				mark(&t.tlsDone)
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if !t.gotConn {
				t.gotConn, t.reused = true, info.Reused
			}
		},
		GotFirstResponseByte: func() { mark(&t.firstByte) },
	}
}

func (t *connTimings) annotate(span trace.Span) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.gotConn {
		span.SetAttributes(attrConnReused.Bool(t.reused))
	}
	phase := func(key attribute.Key, from, to time.Time) {
		if !from.IsZero() && !to.IsZero() {
			span.SetAttributes(key.Float64(milliseconds(to.Sub(from))))
		}
	}
	phase(attrDNS, t.dnsStart, t.dnsDone)
	phase(attrConnect, t.connectStart, t.connectDone)
	phase(attrTLS, t.tlsStart, t.tlsDone)
	phase(attrFirstByte, t.start, t.firstByte)
}

func milliseconds(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// retryReason names why a response or failure is being retried.
func retryReason(statusCode int) string {
	switch statusCode {
	case 0:
		return retryTransport
	case http.StatusTooManyRequests:
		return retryThrottled
	default:
		return retryServerError
	}
}

// noteRetry records a planned wait on the call span and, when a logger is
// configured, as a debug line. statusCode is zero for a transport failure.
// next is the number of the attempt the wait precedes.
func (c *Client) noteRetry(ctx context.Context, call trace.Span, req *http.Request, next, statusCode int, wait retryWait) {
	recording := call.IsRecording()
	if !recording && c.logger == nil {
		return
	}
	reason := retryReason(statusCode)
	if recording {
		attrs := []attribute.KeyValue{
			attrRetryReason.String(reason),
			attrRetryAfter.Bool(wait.retryAfter),
			attrRetryCapped.Bool(wait.capped),
			attrRetryDelay.Float64(milliseconds(wait.delay)),
			attrResendCount.Int(next),
		}
		if statusCode != 0 {
			attrs = append(attrs, attrStatus.Int(statusCode))
		}
		call.AddEvent(eventRetry, trace.WithAttributes(attrs...))
	}
	if c.logger != nil {
		c.logger.LogAttrs(ctx, slog.LevelDebug, "msgraph: retrying request",
			slog.String("method", req.Method),
			slog.String("route", routeTemplate(req.URL.EscapedPath())),
			slog.Int("attempt", next),
			slog.String("reason", reason),
			slog.Int("status", statusCode),
			slog.Bool("retry_after", wait.retryAfter),
			slog.Bool("capped", wait.capped),
			slog.Duration("delay", wait.delay),
		)
	}
}

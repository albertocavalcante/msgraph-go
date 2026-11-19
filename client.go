package msgraph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"
)

const (
	defaultBaseURL       = "https://graph.microsoft.com/v1.0"
	defaultUserAgent     = "msgraph-go"
	defaultRetries       = 3
	defaultMaxRetryDelay = 30 * time.Second
	maxErrorBodyBytes    = 1 << 20
	// defaultMaxResponseBytes bounds a decoded response. The error path was
	// already capped; the success path read whatever arrived, so a large or
	// hostile body was an unbounded allocation. Measured, a 512 MiB response
	// cost 2.2 GiB of heap before this existed, because the read buffer
	// doubles and the decode allocates again on top.
	//
	// 64 MiB is far above any real Graph JSON payload — a full page of
	// messages with bodies is single-digit megabytes — and far below the
	// point where the process is in danger.
	defaultMaxResponseBytes = 64 << 20
)

var errNilTokenSource = errors.New("msgraph: nil token source")

// ErrUntrustedHost is returned when a request URL resolves to a host that is
// neither the Graph base host nor an explicitly allowed host. Every request
// carries a bearer token, so an off-origin URL — a spoofed @odata.nextLink, a
// caller-supplied link, a redirect target written into a response body — would
// hand that token to a third party. Refusing the request is the only safe
// behavior; use [WithAllowedHosts] to opt specific hosts in.
var ErrUntrustedHost = errors.New("msgraph: untrusted request host")

// ErrRedirectRefused is returned when a redirect violates the destination or
// method policy, exceeds the redirect limit, or is rejected by the caller's
// redirect callback. A refused redirect is never retried.
var ErrRedirectRefused = errors.New("msgraph: redirect refused")

// ErrResponseTooLarge is returned when a response body exceeds the configured
// limit. Streaming into an io.Writer is not subject to it: a caller that asks
// to stream has taken responsibility for the size.
var ErrResponseTooLarge = errors.New("msgraph: response body too large")

// Client is a small Microsoft Graph REST client.
type Client struct {
	baseURL            *url.URL
	httpClient         *http.Client
	token              TokenSource
	userAgent          string
	maxRetries         int
	maxDelay           time.Duration
	retryUnsafeMethods bool
	allowedHosts       map[string]struct{}
	maxResponseBytes   int64
	sleep              sleepFunc
	logger             *slog.Logger
}

// Option configures a Client.
type Option func(*Client) error

// WithBaseURL sets the Graph base URL. It is mostly useful for tests and
// sovereign clouds.
func WithBaseURL(value string) Option {
	return func(c *Client) error {
		parsed, err := url.Parse(value)
		if err != nil {
			return errors.New("msgraph: invalid base URL")
		}
		c.baseURL = parsed
		return nil
	}
}

// WithHTTPClient sets the HTTP client. A nil client is ignored. Its
// configuration is copied; the original client's CheckRedirect is not changed.
// Redirect callbacks may further restrict the client's same-origin policy.
// Its transport is used as is: tracing reaches it through the request
// context, never by replacing it.
//
// Without this option every client shares one package-level transport, so
// clients in the same process reuse each other's connections.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) error {
		if client != nil {
			c.httpClient = client
		}
		return nil
	}
}

// WithUserAgent sets the User-Agent header.
func WithUserAgent(value string) Option {
	return func(c *Client) error {
		if value != "" {
			c.userAgent = value
		}
		return nil
	}
}

// WithMaxRetries sets the retry count for throttling and transient server
// errors. Negative values are clamped to zero.
func WithMaxRetries(value int) Option {
	return func(c *Client) error {
		if value < 0 {
			value = 0
		}
		c.maxRetries = value
		return nil
	}
}

// WithMaxRetryDelay caps retry sleeps, including Retry-After values. A
// non-positive value disables the cap.
func WithMaxRetryDelay(value time.Duration) Option {
	return func(c *Client) error {
		c.maxDelay = value
		return nil
	}
}

// WithRetryUnsafeMethods allows retries for non-idempotent HTTP methods. It is
// disabled by default because Graph mutations can have side effects before a
// transient response or connection failure is observed by the client.
func WithRetryUnsafeMethods(value bool) Option {
	return func(c *Client) error {
		c.retryUnsafeMethods = value
		return nil
	}
}

// WithAllowedHosts permits absolute request URLs on additional hosts. The
// [WithBaseURL] host is always allowed. Use this only for hosts that should
// legitimately receive the client's bearer token, such as a sovereign-cloud
// Graph endpoint the caller also talks to directly. Empty values are ignored.
// This permits direct requests only: redirects must remain on the origin of
// the initial request, even when another host is allowed here.
func WithAllowedHosts(hosts ...string) Option {
	return func(c *Client) error {
		for _, host := range hosts {
			host = strings.TrimSpace(host)
			if host == "" {
				continue
			}
			// Accept both bare hosts and full URLs so callers can pass the
			// same string they would give WithBaseURL.
			if parsed, err := url.Parse(host); err == nil && parsed.Host != "" {
				host = parsed.Host
			}
			if c.allowedHosts == nil {
				c.allowedHosts = map[string]struct{}{}
			}
			c.allowedHosts[strings.ToLower(host)] = struct{}{}
		}
		return nil
	}
}

// WithMaxResponseBytes bounds the size of a response the client will decode.
// A non-positive value removes the limit.
//
// The limit does not apply when decoding into an io.Writer, because streaming
// is the supported way to receive something genuinely large.
func WithMaxResponseBytes(limit int64) Option {
	return func(c *Client) error {
		c.maxResponseBytes = limit
		return nil
	}
}

// WithLogger sets a logger for debug-level events that spans do not already
// describe, such as a retry decision and its planned wait. A nil logger, the
// default, disables logging. Log lines carry the same redacted route as spans
// and never a token, body, header, query string or resource identifier.
func WithLogger(logger *slog.Logger) Option {
	return func(c *Client) error {
		c.logger = logger
		return nil
	}
}

// WithSleeper overrides retry sleeping. It exists for tests.
func WithSleeper(fn sleepFunc) Option {
	return func(c *Client) error {
		if fn != nil {
			c.sleep = fn
		}
		return nil
	}
}

// New creates a Microsoft Graph client.
func New(token TokenSource, opts ...Option) (*Client, error) {
	if token == nil {
		return nil, errNilTokenSource
	}
	base, err := url.Parse(defaultBaseURL)
	if err != nil {
		return nil, err
	}
	c := &Client{
		baseURL:          base,
		httpClient:       defaultHTTPClient(),
		token:            token,
		userAgent:        defaultUserAgent,
		maxRetries:       defaultRetries,
		maxDelay:         defaultMaxRetryDelay,
		maxResponseBytes: defaultMaxResponseBytes,
		sleep:            sleepContext,
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	if !validHTTPURL(c.baseURL) || c.baseURL.RawQuery != "" || c.baseURL.ForceQuery {
		return nil, errors.New("msgraph: invalid base URL")
	}
	c.httpClient = c.redirectClient(c.httpClient)
	return c, nil
}

// defaultTransport is shared by every client built without [WithHTTPClient],
// for the reason http.DefaultTransport is shared: a transport per client
// would give each one a private connection pool, and every client in a
// process would pay its own DNS lookup and TLS handshake to the same host.
// It is unexported, so nothing outside the package can reconfigure it.
var defaultTransport = newDefaultTransport()

func newDefaultTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:       10 * time.Second,
		KeepAlive:     30 * time.Second,
		FallbackDelay: 250 * time.Millisecond,
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Graph is one host, so this is effectively the pool size; Go's
		// default of 2 churns connections under concurrent HTTP/1.1 use.
		MaxIdleConnsPerHost: 16,
	}
}

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second, Transport: defaultTransport}
}

// Request describes a Microsoft Graph request.
type Request struct {
	Method           string
	URL              string
	Params           Params
	Query            url.Values
	Header           http.Header
	Prefer           []PreferDirective
	ConsistencyLevel ConsistencyLevel
	// IfMatch sends an If-Match header. Use it with an ETag from a prior
	// [Response] to make an update or delete conditional on the resource not
	// having changed; Graph answers a stale ETag with 412.
	IfMatch string
	// IfNoneMatch sends an If-None-Match header. Graph answers an unchanged
	// resource with 304, which surfaces as [ErrNotModified].
	IfNoneMatch string
	Body        any
	RawBody     []byte
	ContentType string
}

// Response contains metadata from a successful Graph response.
type Response struct {
	StatusCode int
	Header     http.Header
	RequestID  string
	// ETag is the response ETag with quoting preserved, ready to be handed
	// back as [Request.IfMatch].
	ETag string
}

// Get sends a Graph GET request.
func (c *Client) Get(ctx context.Context, path string, params Params, out any) (*Response, error) {
	return c.Do(ctx, Request{Method: http.MethodGet, URL: path, Params: params}, out)
}

// Post sends a Graph POST request.
func (c *Client) Post(ctx context.Context, path string, params Params, body, out any) (*Response, error) {
	return c.Do(ctx, Request{Method: http.MethodPost, URL: path, Params: params, Body: body}, out)
}

// Patch sends a Graph PATCH request.
func (c *Client) Patch(ctx context.Context, path string, params Params, body, out any) (*Response, error) {
	return c.Do(ctx, Request{Method: http.MethodPatch, URL: path, Params: params, Body: body}, out)
}

// Delete sends a Graph DELETE request.
func (c *Client) Delete(ctx context.Context, path string, params Params, out any) (*Response, error) {
	return c.Do(ctx, Request{Method: http.MethodDelete, URL: path, Params: params}, out)
}

// Do sends req and decodes a successful JSON response into out. If out is an
// io.Writer, the response body is streamed into it instead.
func (c *Client) Do(ctx context.Context, req Request, out any) (*Response, error) {
	ctx, span := startSpan(ctx, spanRequest)
	resp, err := c.do(ctx, span, req, out)
	endCall(span, resp, err)
	return resp, err
}

// do is Do without its own span: call is the span of the logical call that
// owns these attempts, and receives their retry events.
func (c *Client) do(ctx context.Context, call trace.Span, req Request, out any) (*Response, error) {
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	if call.IsRecording() {
		call.SetAttributes(attrMethod.String(method))
	}
	body, contentType, err := encodeBody(req)
	if err != nil {
		return nil, err
	}

	canRetry := c.canRetryMethod(method)
	for attempt := 0; ; attempt++ {
		httpReq, err := c.newHTTPRequest(ctx, method, req, body, contentType)
		if err != nil {
			return nil, err
		}
		if attempt == 0 && call.IsRecording() {
			call.SetAttributes(attrURLTemplate.String(routeTemplate(httpReq.URL.EscapedPath())))
		}
		resp, err := roundTrip(c.httpClient, httpReq, attempt, true)
		if err != nil {
			if errors.Is(err, ErrRedirectRefused) || attempt >= c.maxRetries || !canRetry {
				return nil, &requestFailure{cause: err}
			}
			wait := planRetry(nil, attempt, c.maxDelay)
			c.noteRetry(ctx, call, httpReq, attempt+1, 0, wait)
			if err := c.sleep(ctx, wait.delay); err != nil {
				return nil, err
			}
			continue
		}

		if retryableStatus(resp.StatusCode) && attempt < c.maxRetries && canRetry {
			wait := planRetry(resp.Header, attempt, c.maxDelay)
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
			_ = resp.Body.Close()
			c.noteRetry(ctx, call, httpReq, attempt+1, resp.StatusCode, wait)
			if err := c.sleep(ctx, wait.delay); err != nil {
				return nil, err
			}
			continue
		}
		return c.decodeResponse(resp, out)
	}
}

func (c *Client) canRetryMethod(method string) bool {
	if c.retryUnsafeMethods {
		return true
	}
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func (c *Client) newHTTPRequest(
	ctx context.Context,
	method string,
	req Request,
	body []byte,
	contentType string,
) (*http.Request, error) {
	if err := req.Params.Validate(); err != nil {
		return nil, err
	}
	u, err := c.resolveURL(req.URL)
	if err != nil {
		return nil, err
	}
	// Only rebuild the query when there is something to merge. Decoding and
	// re-encoding rewrites the caller's query string — `$skiptoken` becomes
	// `%24skiptoken`, and characters like ] ( * ! get percent-encoded — which
	// matters for @odata.nextLink and @odata.deltaLink. Those are opaque
	// continuation tokens the service handed us to echo back verbatim, and
	// nothing good comes of re-spelling them.
	if params := req.Params.Values(); len(params) > 0 || len(req.Query) > 0 {
		query := u.Query()
		for key, vals := range params {
			for _, val := range vals {
				query.Add(key, val)
			}
		}
		for key, vals := range req.Query {
			for _, val := range vals {
				query.Add(key, val)
			}
		}
		u.RawQuery = query.Encode()
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, errors.New("msgraph: invalid HTTP request")
	}
	// A private per-request binding survives redirects without relying on the
	// mutable request history exposed to a caller's CheckRedirect callback.
	binding := &requestOrigin{origin: url.URL{Scheme: u.Scheme, Host: u.Host}, method: method}
	httpReq = httpReq.WithContext(context.WithValue(httpReq.Context(), requestOriginKey{}, binding))
	token, err := c.acquireToken(ctx)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", c.userAgent)
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	if len(req.Prefer) > 0 {
		httpReq.Header.Add("Prefer", joinPreferDirectives(req.Prefer))
	}
	if req.ConsistencyLevel != "" {
		httpReq.Header.Set("ConsistencyLevel", string(req.ConsistencyLevel))
	}
	if req.IfMatch != "" {
		httpReq.Header.Set("If-Match", req.IfMatch)
	}
	if req.IfNoneMatch != "" {
		httpReq.Header.Set("If-None-Match", req.IfNoneMatch)
	}
	for key, vals := range req.Header {
		for _, val := range vals {
			httpReq.Header.Add(key, val)
		}
	}
	return httpReq, nil
}

func (c *Client) resolveURL(value string) (*url.URL, error) {
	if value == "" {
		return nil, errors.New("msgraph: empty request url")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, errors.New("msgraph: invalid request URL")
	}
	if parsed.IsAbs() {
		if !validHTTPURL(parsed) {
			return nil, errors.New("msgraph: invalid request URL")
		}
		if !c.hostAllowed(parsed) {
			return nil, ErrUntrustedHost
		}
		return parsed, nil
	}
	if parsed.Host != "" {
		return nil, ErrUntrustedHost
	}
	if parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return nil, errors.New("msgraph: invalid request URL")
	}
	base := *c.baseURL
	basePath := strings.TrimRight(base.Path, "/")
	baseEscapedPath := strings.TrimRight(base.EscapedPath(), "/")
	requestPath := strings.TrimLeft(parsed.Path, "/")
	requestEscapedPath := strings.TrimLeft(parsed.EscapedPath(), "/")
	base.Path = basePath + "/" + requestPath
	if requestEscapedPath != requestPath || baseEscapedPath != basePath {
		base.RawPath = baseEscapedPath + "/" + requestEscapedPath
	}
	base.RawQuery = parsed.RawQuery
	return &base, nil
}

// hostAllowed reports whether an absolute URL may receive the bearer token. A
// scheme change is rejected as well as a host change, so an https base URL can
// never be downgraded to plaintext http by a link in a response body.
func (c *Client) hostAllowed(u *url.URL) bool {
	if !strings.EqualFold(u.Scheme, c.baseURL.Scheme) {
		return false
	}
	if sameOrigin(u, c.baseURL) {
		return true
	}
	for host := range c.allowedHosts {
		if sameOrigin(u, &url.URL{Scheme: c.baseURL.Scheme, Host: host}) {
			return true
		}
	}
	return false
}

func encodeBody(req Request) (body []byte, contentType string, err error) {
	if req.RawBody != nil {
		return req.RawBody, req.ContentType, nil
	}
	if req.Body == nil {
		return nil, "", nil
	}
	body, err = json.Marshal(req.Body)
	if err != nil {
		return nil, "", fmt.Errorf("marshal request body: %w", err)
	}
	contentType = req.ContentType
	if contentType == "" {
		contentType = "application/json"
	}
	return body, contentType, nil
}

// maxDrainBytes bounds the unread body the client reads to keep its
// connection. Reading 64 KiB costs less than a new TCP and TLS handshake; a
// larger or unannounced body may be slow or endless, and is cheaper to drop.
const maxDrainBytes = 64 << 10

// drainSmallBody reads a body nobody asked for when its declared length is
// small, so closing it returns the connection to the pool instead of
// discarding it. Any other body is left for the caller's Close, so draining
// never waits on a slow or unbounded body. Read errors do not matter here.
func drainSmallBody(resp *http.Response) {
	if resp.ContentLength > 0 && resp.ContentLength <= maxDrainBytes {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, resp.ContentLength))
	}
}

func (c *Client) decodeResponse(resp *http.Response, out any) (*Response, error) {
	defer resp.Body.Close()
	meta := &Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		RequestID:  firstHeader(resp.Header, "request-id", "client-request-id"),
		ETag:       resp.Header.Get("ETag"),
	}
	// A conditional GET that matched is a successful outcome with no body, not
	// a failure, so it gets its own sentinel instead of an APIError.
	if resp.StatusCode == http.StatusNotModified {
		return meta, ErrNotModified
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		if err != nil {
			return nil, fmt.Errorf("read error response: %w", err)
		}
		return nil, parseAPIError(resp.StatusCode, resp.Header, body)
	}
	if out == nil {
		drainSmallBody(resp)
		return meta, nil
	}
	if writer, ok := out.(io.Writer); ok {
		if _, err := io.Copy(writer, resp.Body); err != nil {
			return meta, fmt.Errorf("stream response: %w", err)
		}
		return meta, nil
	}
	reader := io.Reader(resp.Body)
	if c.maxResponseBytes > 0 {
		// Read one byte past the limit so exceeding it is detectable rather
		// than silently truncating the payload into a decode error.
		reader = io.LimitReader(resp.Body, c.maxResponseBytes+1)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if c.maxResponseBytes > 0 && int64(len(body)) > c.maxResponseBytes {
		return nil, fmt.Errorf("%w: over %d bytes", ErrResponseTooLarge, c.maxResponseBytes)
	}
	if len(body) == 0 {
		return meta, nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return meta, nil
}

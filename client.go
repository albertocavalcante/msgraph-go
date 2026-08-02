package msgraph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBaseURL       = "https://graph.microsoft.com/v1.0"
	defaultUserAgent     = "msgraph-go"
	defaultRetries       = 3
	defaultMaxRetryDelay = 30 * time.Second
	maxErrorBodyBytes    = 1 << 20
)

var errNilTokenSource = errors.New("msgraph: nil token source")

// Client is a small Microsoft Graph REST client.
type Client struct {
	baseURL            *url.URL
	httpClient         *http.Client
	token              TokenSource
	userAgent          string
	maxRetries         int
	maxDelay           time.Duration
	retryUnsafeMethods bool
	sleep              sleepFunc
}

// Option configures a Client.
type Option func(*Client) error

// WithBaseURL sets the Graph base URL. It is mostly useful for tests and
// sovereign clouds.
func WithBaseURL(value string) Option {
	return func(c *Client) error {
		parsed, err := url.Parse(value)
		if err != nil {
			return fmt.Errorf("parse base url: %w", err)
		}
		c.baseURL = parsed
		return nil
	}
}

// WithHTTPClient sets the HTTP client. A nil client is ignored.
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
		baseURL:    base,
		httpClient: defaultHTTPClient(),
		token:      token,
		userAgent:  defaultUserAgent,
		maxRetries: defaultRetries,
		maxDelay:   defaultMaxRetryDelay,
		sleep:      sleepContext,
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func defaultHTTPClient() *http.Client {
	dialer := &net.Dialer{
		Timeout:       10 * time.Second,
		KeepAlive:     30 * time.Second,
		FallbackDelay: 250 * time.Millisecond,
	}
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// Request describes a Microsoft Graph request.
type Request struct {
	Method           string
	URL              string
	Params           Params
	Query            url.Values
	Header           http.Header
	Prefer           []string
	ConsistencyLevel string
	Body             any
	RawBody          []byte
	ContentType      string
}

// Response contains metadata from a successful Graph response.
type Response struct {
	StatusCode int
	Header     http.Header
	RequestID  string
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
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	body, contentType, err := encodeBody(req)
	if err != nil {
		return nil, err
	}

	var lastErr error
	canRetry := c.canRetryMethod(method)
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		httpReq, err := c.newHTTPRequest(ctx, method, req, body, contentType)
		if err != nil {
			return nil, err
		}
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			lastErr = err
			if attempt == c.maxRetries || !canRetry {
				return nil, err
			}
			if err := c.sleep(ctx, retryDelay(nil, attempt, c.maxDelay)); err != nil {
				return nil, err
			}
			continue
		}

		if retryableStatus(resp.StatusCode) && attempt < c.maxRetries && canRetry {
			delay := retryDelay(resp.Header, attempt, c.maxDelay)
			_ = resp.Body.Close()
			if err := c.sleep(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}
		return decodeResponse(resp, out)
	}
	return nil, lastErr
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
	token, err := c.token.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	u, err := c.resolveURL(req.URL)
	if err != nil {
		return nil, err
	}
	query := u.Query()
	for key, vals := range req.Params.Values() {
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

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", c.userAgent)
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	if len(req.Prefer) > 0 {
		httpReq.Header.Add("Prefer", strings.Join(req.Prefer, ", "))
	}
	if req.ConsistencyLevel != "" {
		httpReq.Header.Set("ConsistencyLevel", req.ConsistencyLevel)
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
		return nil, fmt.Errorf("parse request url: %w", err)
	}
	if parsed.IsAbs() {
		return parsed, nil
	}
	base := *c.baseURL
	base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(parsed.Path, "/")
	base.RawQuery = parsed.RawQuery
	return &base, nil
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

func decodeResponse(resp *http.Response, out any) (*Response, error) {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		if err != nil {
			return nil, fmt.Errorf("read error response: %w", err)
		}
		return nil, parseAPIError(resp.StatusCode, resp.Header, body)
	}
	meta := &Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		RequestID:  firstHeader(resp.Header, "request-id", "client-request-id"),
	}
	if out == nil {
		return meta, nil
	}
	if writer, ok := out.(io.Writer); ok {
		if _, err := io.Copy(writer, resp.Body); err != nil {
			return meta, fmt.Errorf("stream response: %w", err)
		}
		return meta, nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) == 0 {
		return meta, nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return meta, nil
}

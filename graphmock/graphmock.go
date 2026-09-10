// Package graphmock provides a typed fake Microsoft Graph endpoint for tests.
//
// Handlers return Go values rather than JSON strings, so a response is built
// through the same marshaling the production code uses. A hand-written string
// literal cannot drift-check against a model: rename a field or change a tag
// and the literal keeps parsing, so the test keeps passing while the code is
// wrong.
//
// The second purpose matters more. A fake that agrees with everything is a
// mirror, not a test. Real Graph refuses filters that OData permits, pages
// delta far smaller than callers expect, and omits headers callers assume are
// there. [ExchangeMail] encodes those refusals, so a request the service would
// reject is rejected here too, at the speed of a unit test.
package graphmock

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	msgraph "github.com/albertocavalcante/msgraph-go"
)

// Server is a fake Graph endpoint.
type Server struct {
	tb          testing.TB
	mux         *http.ServeMux
	http        *httptest.Server
	constraints []Constraint

	mu   sync.Mutex
	seen []Recorded
}

// Option configures a [Server].
type Option func(*Server)

// WithConstraints makes the fake reject requests the real service would.
func WithConstraints(constraints ...Constraint) Option {
	return func(s *Server) { s.constraints = append(s.constraints, constraints...) }
}

// New starts a fake Graph endpoint and stops it when the test finishes.
func New(tb testing.TB, opts ...Option) *Server {
	tb.Helper()
	server := &Server{tb: tb, mux: http.NewServeMux()}
	for _, opt := range opts {
		opt(server)
	}
	server.http = httptest.NewServer(server)
	tb.Cleanup(server.http.Close)
	return server
}

// URL is the base URL to point a client at.
func (s *Server) URL() string { return s.http.URL }

// Client returns a Graph client wired to this server with a static token.
func (s *Server) Client(opts ...msgraph.Option) *msgraph.Client {
	s.tb.Helper()
	opts = append([]msgraph.Option{msgraph.WithBaseURL(s.URL())}, opts...)
	client, err := msgraph.New(msgraph.TokenSourceFunc(func(ctx context.Context) (string, error) {
		return "graphmock-token", nil
	}), opts...)
	if err != nil {
		s.tb.Fatalf("graphmock: build client: %v", err)
	}
	return client
}

// Recorded is one request the server received.
type Recorded struct {
	Method string
	Path   string
	Query  Query
	Header http.Header
	Body   []byte
}

// Requests returns every request received so far, oldest first.
func (s *Server) Requests() []Recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Recorded(nil), s.seen...)
}

// Reset discards recorded requests, for a test that runs several commands.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = nil
}

// Request is what a handler is given.
type Request struct {
	Method string
	// Path is the escaped request path.
	Path   string
	Query  Query
	Header http.Header
	Body   []byte

	raw *http.Request
}

// PathValue returns a wildcard captured by the route pattern, such as the id
// in "GET /me/messages/{id}".
func (r *Request) PathValue(name string) string { return r.raw.PathValue(name) }

// Decode unmarshals the request body into v, failing the test if it cannot.
func Decode[T any](tb testing.TB, r *Request) T {
	tb.Helper()
	var value T
	if err := json.Unmarshal(r.Body, &value); err != nil {
		tb.Fatalf("graphmock: decode request body: %v\nbody: %s", err, r.Body)
	}
	return value
}

// Query is the parsed OData query string.
type Query struct {
	Select     []string
	Filter     string
	OrderBy    []string
	Search     string
	Top        int
	Skip       int
	Count      bool
	SkipToken  string
	DeltaToken string
	Raw        url.Values
}

// Has reports whether a raw query parameter was sent.
func (q Query) Has(name string) bool { return q.Raw.Has(name) }

func parseQuery(values url.Values) Query {
	query := Query{
		Filter:     values.Get("$filter"),
		Search:     values.Get("$search"),
		SkipToken:  values.Get("$skiptoken"),
		DeltaToken: values.Get("$deltatoken"),
		Count:      values.Get("$count") == "true",
		Raw:        values,
	}
	if v := values.Get("$select"); v != "" {
		query.Select = strings.Split(v, ",")
	}
	if v := values.Get("$orderby"); v != "" {
		query.OrderBy = strings.Split(v, ",")
	}
	query.Top, _ = strconv.Atoi(values.Get("$top"))
	query.Skip, _ = strconv.Atoi(values.Get("$skip"))
	return query
}

// PageSize returns the odata.maxpagesize the caller asked for, and whether one
// was sent at all.
func (r *Request) PageSize() (int, bool) {
	for _, value := range r.Header.Values("Prefer") {
		for _, directive := range strings.Split(value, ",") {
			directive = strings.TrimSpace(directive)
			rest, ok := strings.CutPrefix(directive, "odata.maxpagesize=")
			if !ok {
				continue
			}
			if size, err := strconv.Atoi(rest); err == nil {
				return size, true
			}
		}
	}
	return 0, false
}

// Prefers reports whether the request carried a Prefer directive.
func (r *Request) Prefers(directive msgraph.PreferDirective) bool {
	for _, value := range r.Header.Values("Prefer") {
		for _, part := range strings.Split(value, ",") {
			if strings.TrimSpace(part) == string(directive) {
				return true
			}
		}
	}
	return false
}

// Response is what a handler returns.
type Response struct {
	Status int
	Header http.Header
	body   []byte
}

// Item responds with a single typed resource.
func Item[T any](value T) Response { return marshal(http.StatusOK, value) }

// Created responds 201 with a typed resource.
func Created[T any](value T) Response { return marshal(http.StatusCreated, value) }

// Collection responds with a Graph collection page. A non-empty nextLink is
// echoed as @odata.nextLink; use [Delta] for a terminal delta page.
func Collection[T any](items []T, nextLink string) Response {
	return marshal(http.StatusOK, msgraph.Page[T]{Value: items, NextLink: nextLink})
}

// Delta responds with a delta page carrying a deltaLink, marking the end of a
// change set.
func Delta[T any](items []T, deltaLink string) Response {
	return marshal(http.StatusOK, msgraph.Page[T]{Value: items, DeltaLink: deltaLink})
}

// Raw responds with an arbitrary body and content type, for the endpoints that
// do not speak JSON — a message's MIME representation, or an attachment's
// bytes.
func Raw(status int, contentType string, body []byte) Response {
	response := Response{Status: status, body: body}
	if contentType != "" {
		response.Header = http.Header{"Content-Type": []string{contentType}}
	}
	return response
}

// Empty responds with a status and no body, for the 202 and 204 replies Graph
// gives to sends, moves, and deletes.
func Empty(status int) Response { return Response{Status: status} }

// Fault responds with a Graph error envelope.
func Fault(status int, code, message string) Response {
	return marshal(status, errorEnvelope{Error: errorBody{Code: code, Message: message}})
}

// Throttled responds 429 with a Retry-After header.
func Throttled(retryAfterSeconds int) Response {
	response := Fault(http.StatusTooManyRequests, "TooManyRequests", "throttled")
	response.Header = http.Header{"Retry-After": []string{strconv.Itoa(retryAfterSeconds)}}
	return response
}

// WithHeader adds a response header.
func (r Response) WithHeader(name, value string) Response {
	if r.Header == nil {
		r.Header = http.Header{}
	}
	r.Header.Add(name, value)
	return r
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func marshal(status int, value any) Response {
	body, err := json.Marshal(value)
	if err != nil {
		return Fault(http.StatusInternalServerError, "MockMarshalFailed", err.Error())
	}
	return Response{Status: status, body: body}
}

// Handler answers a request.
type Handler func(*Request) Response

// Handle registers a handler for a method-and-path pattern using the standard
// library's routing syntax, so "GET /me/messages/{id}" captures an id.
func (s *Server) Handle(pattern string, handler Handler) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		body, err := readBody(r)
		if err != nil {
			s.tb.Fatalf("graphmock: read request body: %v", err)
		}
		request := &Request{
			Method: r.Method,
			Path:   r.URL.EscapedPath(),
			Query:  parseQuery(r.URL.Query()),
			Header: r.Header.Clone(),
			Body:   body,
			raw:    r,
		}

		s.mu.Lock()
		s.seen = append(s.seen, Recorded{
			Method: request.Method,
			Path:   request.Path,
			Query:  request.Query,
			Header: request.Header,
			Body:   request.Body,
		})
		s.mu.Unlock()

		response := s.enforce(request)
		if response == nil {
			answered := handler(request)
			response = &answered
		}
		write(w, *response)
	})
}

// enforce applies the configured constraints, returning the first rejection.
func (s *Server) enforce(r *Request) *Response {
	for _, constraint := range s.constraints {
		if response := constraint(r); response != nil {
			return response
		}
	}
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func write(w http.ResponseWriter, response Response) {
	for name, values := range response.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	if len(response.body) > 0 && w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	status := response.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if len(response.body) > 0 {
		_, _ = w.Write(response.body)
	}
}

func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

// Link builds an absolute link back into this server, for a nextLink or a
// deltaLink. The Graph client refuses to follow a link to another host, so a
// test must not invent one.
func (s *Server) Link(path string, query ...string) string {
	link := s.URL() + path
	if len(query) > 0 {
		link += "?" + strings.Join(query, "&")
	}
	return link
}

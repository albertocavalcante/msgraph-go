package msgraph

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const defaultMaxRedirects = 10

var (
	errUnsafeRedirect = errors.New("msgraph: unsafe request redirect")
	errRedirectLimit  = errors.New("msgraph: redirect limit reached")
)

type requestOriginKey struct{}

// requestOrigin is private immutable provenance for a single initial request.
// It deliberately contains no path, query, token or other account data.
type requestOrigin struct {
	origin url.URL
	method string
}

func originBinding(req *http.Request) *requestOrigin {
	if req == nil {
		return nil
	}
	binding, _ := req.Context().Value(requestOriginKey{}).(*requestOrigin)
	return binding
}

func unchangedBinding(req *http.Request, via []*http.Request, binding *requestOrigin) bool {
	if binding == nil || originBinding(req) != binding {
		return false
	}
	for _, previous := range via {
		if originBinding(previous) != binding {
			return false
		}
	}
	return true
}

// redirectClient copies configuration, retaining the caller's transport, jar
// and timeout. Only the copy's redirect policy is wrapped.
func (c *Client) redirectClient(original *http.Client) *http.Client {
	client := *original
	check := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		// Never derive provenance from via[0].URL: a previous callback could
		// have changed it while leaving the first redirect destination intact.
		binding := originBinding(req)
		if len(via) == 0 || !unchangedBinding(req, via, binding) {
			return &redirectFailure{cause: ErrUntrustedHost}
		}
		if err := redirectDestination(req, &binding.origin, binding.method); err != nil {
			return &redirectFailure{cause: err}
		}
		if check != nil {
			if err := check(req, via); err != nil {
				if errors.Is(err, http.ErrUseLastResponse) {
					return http.ErrUseLastResponse
				}
				return &redirectFailure{cause: err}
			}
		} else if len(via) >= defaultMaxRedirects {
			return &redirectFailure{cause: errRedirectLimit}
		}
		// A callback cannot turn an approved redirect into a request to
		// another origin or an unsafe method after the first validation.
		if !unchangedBinding(req, via, binding) {
			return &redirectFailure{cause: ErrUntrustedHost}
		}
		if err := redirectDestination(req, &binding.origin, binding.method); err != nil {
			return &redirectFailure{cause: err}
		}
		return nil
	}
	return &client
}

func redirectDestination(req *http.Request, origin *url.URL, initialMethod string) error {
	if !validHTTPURL(req.URL) || !sameOrigin(req.URL, origin) {
		return ErrUntrustedHost
	}
	if req.Host != "" && !sameOrigin(&url.URL{Scheme: req.URL.Scheme, Host: req.Host}, origin) {
		return ErrUntrustedHost
	}
	if !safeRedirectMethod(initialMethod) || !safeRedirectMethod(req.Method) {
		return errUnsafeRedirect
	}
	return nil
}

func safeRedirectMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func validHTTPURL(u *url.URL) bool {
	if u == nil || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.Hostname() == "" {
		return false
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	_, ok := originPort(u)
	return ok
}

func sameOrigin(a, b *url.URL) bool {
	if !validHTTPURL(a) || !validHTTPURL(b) || !strings.EqualFold(a.Scheme, b.Scheme) ||
		!strings.EqualFold(a.Hostname(), b.Hostname()) {
		return false
	}
	aPort, _ := originPort(a)
	bPort, _ := originPort(b)
	return aPort == bPort
}

func originPort(u *url.URL) (uint64, bool) {
	port := u.Port()
	if port == "" {
		// An explicit empty port is ambiguous rather than a default port.
		if strings.HasSuffix(u.Host, ":") {
			return 0, false
		}
		if strings.EqualFold(u.Scheme, "https") {
			return 443, true
		}
		return 80, true
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	return parsed, err == nil
}

type redirectFailure struct{ cause error }

func (e *redirectFailure) Error() string {
	switch {
	case errors.Is(e.cause, ErrUntrustedHost):
		return "msgraph: redirect refused: untrusted destination"
	case errors.Is(e.cause, errUnsafeRedirect):
		return "msgraph: redirect refused: unsafe method"
	case errors.Is(e.cause, errRedirectLimit):
		return "msgraph: redirect refused: limit reached"
	default:
		return "msgraph: redirect refused by caller policy"
	}
}

func (e *redirectFailure) Unwrap() []error { return []error{ErrRedirectRefused, e.cause} }

// http.Client wraps failures in a url.Error whose string includes the entire
// URL, including opaque delta tokens. Retain the inspectable cause while
// keeping the ordinary error message free of request URLs and callback text.
type requestFailure struct{ cause error }

func (e *requestFailure) Error() string {
	var redirect *redirectFailure
	if errors.As(e.cause, &redirect) {
		return redirect.Error()
	}
	if errors.Is(e.cause, context.Canceled) {
		return "msgraph: HTTP request canceled"
	}
	if errors.Is(e.cause, context.DeadlineExceeded) {
		return "msgraph: HTTP request timed out"
	}
	return "msgraph: HTTP request failed"
}

func (e *requestFailure) Unwrap() error { return e.cause }

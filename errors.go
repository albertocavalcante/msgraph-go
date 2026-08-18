package msgraph

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ErrRequestFailed is wrapped by [APIError] for non-2xx Graph responses.
var ErrRequestFailed = errors.New("msgraph: request failed")

// ErrNotModified is returned when a conditional request matched and Graph
// answered 304. It is an outcome rather than a failure: the accompanying
// [Response] is still valid, and no body was sent.
var ErrNotModified = errors.New("msgraph: not modified")

// ErrorDetail is one entry from a Graph error's details array.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Target  string `json:"target"`
}

// APIError is a structured Microsoft Graph error response.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	// Target names the request property the error is about, when Graph says.
	Target string
	// Details holds per-property errors for multi-part validation failures.
	Details   []ErrorDetail
	RequestID string
	// RetryAfter is the parsed Retry-After header. It is zero when absent,
	// and is only meaningful for throttling and transient server errors.
	RetryAfter time.Duration
	Raw        []byte
}

func (e *APIError) Error() string {
	if e.Code == "" && e.Message == "" {
		return fmt.Sprintf("%v: status %d", ErrRequestFailed, e.StatusCode)
	}
	if e.Code == "" {
		return fmt.Sprintf("%v: status %d: %s", ErrRequestFailed, e.StatusCode, e.Message)
	}
	if e.Message == "" {
		return fmt.Sprintf("%v: status %d: %s", ErrRequestFailed, e.StatusCode, e.Code)
	}
	return fmt.Sprintf("%v: status %d: %s: %s", ErrRequestFailed, e.StatusCode, e.Code, e.Message)
}

func (e *APIError) Unwrap() error {
	return ErrRequestFailed
}

// IsThrottled reports whether err is a Graph throttling response.
func IsThrottled(err error) bool {
	return IsStatus(err, http.StatusTooManyRequests)
}

// IsNotFound reports whether err is a Graph 404 response.
func IsNotFound(err error) bool {
	return IsStatus(err, http.StatusNotFound)
}

// IsUnauthorized reports whether err is a Graph 401 response.
func IsUnauthorized(err error) bool {
	return IsStatus(err, http.StatusUnauthorized)
}

// IsForbidden reports whether err is a Graph 403 response.
func IsForbidden(err error) bool {
	return IsStatus(err, http.StatusForbidden)
}

// IsPreconditionFailed reports whether err is a Graph 412 response, which is
// how a stale [Request.IfMatch] ETag surfaces.
func IsPreconditionFailed(err error) bool {
	return IsStatus(err, http.StatusPreconditionFailed)
}

// IsConflict reports whether err is a Graph 409 response.
func IsConflict(err error) bool {
	return IsStatus(err, http.StatusConflict)
}

// IsNotModified reports whether err is [ErrNotModified].
func IsNotModified(err error) bool {
	return errors.Is(err, ErrNotModified)
}

// IsStatus reports whether err is a Graph response with statusCode.
func IsStatus(err error, statusCode int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == statusCode
}

// HasCode reports whether err is a Graph error carrying the given service
// error code, such as "ErrorItemNotFound". Comparison is case-insensitive
// because Graph is not consistent about casing across workloads.
func HasCode(err error, code string) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if strings.EqualFold(apiErr.Code, code) {
		return true
	}
	for _, detail := range apiErr.Details {
		if strings.EqualFold(detail.Code, code) {
			return true
		}
	}
	return false
}

type graphErrorEnvelope struct {
	Error graphErrorBody `json:"error"`
}

type graphErrorBody struct {
	Code       string          `json:"code"`
	Message    string          `json:"message"`
	Target     string          `json:"target"`
	Details    []ErrorDetail   `json:"details"`
	InnerError json.RawMessage `json:"innerError"`
}

type innerGraphError struct {
	RequestID       string          `json:"request-id"`
	RequestIDCamel  string          `json:"requestId"`
	ClientRequestID string          `json:"client-request-id"`
	InnerError      json.RawMessage `json:"innerError"`
}

func parseAPIError(statusCode int, header http.Header, body []byte) *APIError {
	apiErr := &APIError{
		StatusCode: statusCode,
		RequestID:  firstHeader(header, "request-id", "client-request-id", "x-ms-ags-diagnostic"),
		Raw:        body,
	}
	if delay, ok := retryAfterDelay(header); ok {
		apiErr.RetryAfter = delay
	}
	var envelope graphErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil {
		apiErr.Code = envelope.Error.Code
		apiErr.Message = envelope.Error.Message
		apiErr.Target = envelope.Error.Target
		apiErr.Details = envelope.Error.Details
		if apiErr.RequestID == "" {
			apiErr.RequestID = requestIDFromInner(envelope.Error.InnerError)
		}
	}
	return apiErr
}

func requestIDFromInner(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var fields innerGraphError
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ""
	}
	switch {
	case fields.RequestID != "":
		return fields.RequestID
	case fields.RequestIDCamel != "":
		return fields.RequestIDCamel
	case fields.ClientRequestID != "":
		return fields.ClientRequestID
	case len(fields.InnerError) > 0:
		return requestIDFromInner(fields.InnerError)
	default:
		return ""
	}
}

func firstHeader(header http.Header, names ...string) string {
	for _, name := range names {
		if val := header.Get(name); val != "" {
			return val
		}
	}
	return ""
}

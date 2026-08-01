package msgraph

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

var errGraphRequestFailed = errors.New("msgraph: request failed")

// APIError is a structured Microsoft Graph error response.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
	Raw        []byte
}

func (e *APIError) Error() string {
	if e.Code == "" && e.Message == "" {
		return fmt.Sprintf("%v: status %d", errGraphRequestFailed, e.StatusCode)
	}
	if e.Code == "" {
		return fmt.Sprintf("%v: status %d: %s", errGraphRequestFailed, e.StatusCode, e.Message)
	}
	if e.Message == "" {
		return fmt.Sprintf("%v: status %d: %s", errGraphRequestFailed, e.StatusCode, e.Code)
	}
	return fmt.Sprintf("%v: status %d: %s: %s", errGraphRequestFailed, e.StatusCode, e.Code, e.Message)
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

// IsStatus reports whether err is a Graph response with statusCode.
func IsStatus(err error, statusCode int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == statusCode
}

type graphErrorEnvelope struct {
	Error graphErrorBody `json:"error"`
}

type graphErrorBody struct {
	Code       string          `json:"code"`
	Message    string          `json:"message"`
	InnerError json.RawMessage `json:"innerError"`
}

func parseAPIError(statusCode int, header http.Header, body []byte) *APIError {
	apiErr := &APIError{
		StatusCode: statusCode,
		RequestID:  firstHeader(header, "request-id", "client-request-id", "x-ms-ags-diagnostic"),
		Raw:        body,
	}
	var envelope graphErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil {
		apiErr.Code = envelope.Error.Code
		apiErr.Message = envelope.Error.Message
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
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ""
	}
	for _, key := range []string{"request-id", "requestId", "client-request-id"} {
		if val, ok := fields[key].(string); ok {
			return val
		}
	}
	return ""
}

func firstHeader(header http.Header, names ...string) string {
	for _, name := range names {
		if val := header.Get(name); val != "" {
			return val
		}
	}
	return ""
}

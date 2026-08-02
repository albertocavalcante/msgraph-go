package msgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const maxBatchRequests = 20

var (
	errEmptyBatch       = errors.New("msgraph: batch requires at least one request")
	errBatchTooLarge    = errors.New("msgraph: batch has more than 20 requests")
	errBatchMissingID   = errors.New("msgraph: batch request missing id")
	errBatchMissingURL  = errors.New("msgraph: batch request missing url")
	errBatchMissingVerb = errors.New("msgraph: batch request missing method")
)

// BatchRequest is one request inside a Microsoft Graph JSON batch.
type BatchRequest struct {
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    any               `json:"body,omitempty"`
}

// BatchResponse is one response inside a Microsoft Graph JSON batch.
type BatchResponse struct {
	ID      string            `json:"id"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// BatchError reports one or more failed subresponses from a strict batch call.
type BatchError struct {
	Responses []BatchResponse
}

func (e *BatchError) Error() string {
	if len(e.Responses) == 1 {
		return fmt.Sprintf("msgraph: batch subrequest %q failed with status %d", e.Responses[0].ID, e.Responses[0].Status)
	}
	return fmt.Sprintf("msgraph: %d batch subrequests failed", len(e.Responses))
}

type batchEnvelope struct {
	Requests []BatchRequest `json:"requests"`
}

type batchResult struct {
	Responses []BatchResponse `json:"responses"`
}

// Batch sends a Microsoft Graph JSON batch request.
func (c *Client) Batch(ctx context.Context, requests []BatchRequest) ([]BatchResponse, error) {
	if len(requests) == 0 {
		return nil, errEmptyBatch
	}
	if len(requests) > maxBatchRequests {
		return nil, errBatchTooLarge
	}
	normalized := make([]BatchRequest, len(requests))
	for i, req := range requests {
		if req.ID == "" {
			return nil, fmt.Errorf("%w at index %d", errBatchMissingID, i)
		}
		if req.Method == "" {
			return nil, fmt.Errorf("%w at index %d", errBatchMissingVerb, i)
		}
		if req.URL == "" {
			return nil, fmt.Errorf("%w at index %d", errBatchMissingURL, i)
		}
		batchURL, err := c.normalizeBatchURL(req.URL)
		if err != nil {
			return nil, fmt.Errorf("batch request %q url: %w", req.ID, err)
		}
		req.URL = batchURL
		normalized[i] = req
	}

	var result batchResult
	if _, err := c.Post(ctx, "/$batch", Params{}, batchEnvelope{Requests: normalized}, &result); err != nil {
		return nil, err
	}
	return result.Responses, nil
}

// BatchStrict sends a JSON batch and returns a [BatchError] when any
// subrequest status is outside 2xx. The full response slice is still returned
// so callers can inspect successes and failures together.
func (c *Client) BatchStrict(ctx context.Context, requests []BatchRequest) ([]BatchResponse, error) {
	responses, err := c.Batch(ctx, requests)
	if err != nil {
		return nil, err
	}
	failed := FailedBatchResponses(responses)
	if len(failed) > 0 {
		return responses, &BatchError{Responses: failed}
	}
	return responses, nil
}

// FailedBatchResponses returns every subresponse with a non-2xx status.
func FailedBatchResponses(responses []BatchResponse) []BatchResponse {
	var failed []BatchResponse
	for _, resp := range responses {
		if resp.Status < 200 || resp.Status > 299 {
			failed = append(failed, resp)
		}
	}
	return failed
}

func (c *Client) normalizeBatchURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	if !parsed.IsAbs() {
		return value, nil
	}
	if !sameOrigin(parsed, c.baseURL) {
		return "", fmt.Errorf("absolute URL origin %q does not match Graph base origin %q", parsed.Scheme+"://"+parsed.Host, c.baseURL.Scheme+"://"+c.baseURL.Host)
	}
	relative := stripBasePath(parsed.EscapedPath(), c.baseURL.EscapedPath())
	if parsed.RawQuery != "" {
		relative += "?" + parsed.RawQuery
	}
	if relative == "" {
		return "", errBatchMissingURL
	}
	return relative, nil
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func stripBasePath(pathValue, basePath string) string {
	basePath = strings.TrimRight(basePath, "/")
	if basePath == "" {
		return pathValue
	}
	if pathValue == basePath {
		return "/"
	}
	if strings.HasPrefix(pathValue, basePath+"/") {
		return strings.TrimPrefix(pathValue, basePath)
	}
	return pathValue
}

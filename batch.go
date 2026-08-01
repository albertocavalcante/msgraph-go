package msgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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
		batchURL, err := normalizeBatchURL(req.URL)
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

func normalizeBatchURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	if !parsed.IsAbs() {
		return value, nil
	}
	relative := parsed.EscapedPath()
	if parsed.RawQuery != "" {
		relative += "?" + parsed.RawQuery
	}
	if relative == "" {
		return "", errBatchMissingURL
	}
	return relative, nil
}

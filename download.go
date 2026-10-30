package msgraph

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Download streams a Graph file or format-conversion response into out.
//
// Graph's /content endpoints may answer 302 with a short-lived, preauthenticated
// HTTPS URL. Download makes a separate GET to that URL without the Graph bearer
// token, cookies, or other request headers. It never follows another redirect.
// The ordinary Do/Get redirect policy is unchanged. HTTP client redirect
// callbacks are not used: both requests explicitly disable automatic redirects.
//
// Params.Custom can request conversion, for example format=pdf. The configured
// WithMaxResponseBytes limit also bounds downloads (64 MiB by default). An error
// may leave partial bytes in out; file callers should stage output and publish it
// only after success. Graph throttling is retried before streaming; content
// requests and partial streams are never retried.
func (c *Client) Download(ctx context.Context, path string, params Params, out io.Writer) (*Response, error) {
	if out == nil {
		return nil, errors.New("msgraph: nil download writer")
	}
	// A copied client keeps the transport and timeout, while preventing net/http
	// from following a signed URL with authentication or ambient cookies.
	client := *c.httpClient
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, err := c.newHTTPRequest(ctx, http.MethodGet, Request{URL: path, Params: params}, nil, "")
		if err != nil {
			return nil, err
		}
		resp, err = client.Do(req)
		if err != nil {
			return nil, &requestFailure{cause: err}
		}
		if !retryableStatus(resp.StatusCode) || attempt >= c.maxRetries {
			break
		}
		delay := retryDelay(resp.Header, attempt, c.maxDelay)
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		_ = resp.Body.Close()
		if err := c.sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
	if resp.StatusCode != http.StatusFound {
		return c.downloadBody(resp, out, true)
	}
	target, err := resp.Location()
	_ = resp.Body.Close()
	if err != nil || !validHTTPURL(target) || !strings.EqualFold(target.Scheme, "https") {
		return nil, &redirectFailure{cause: ErrUntrustedHost}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), http.NoBody)
	if err != nil {
		return nil, errors.New("msgraph: invalid content download request")
	}
	// Deliberately do not copy Graph headers, the caller's cookie jar, or a
	// Referer containing the signed URL to this independent request.
	resp, err = client.Do(req)
	if err != nil {
		return nil, &requestFailure{cause: err}
	}
	return c.downloadBody(resp, out, false)
}

func (c *Client) downloadBody(resp *http.Response, out io.Writer, graph bool) (*Response, error) {
	defer resp.Body.Close()
	meta := &Response{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), RequestID: firstHeader(resp.Header, "request-id", "client-request-id"), ETag: resp.Header.Get("ETag")}
	if resp.StatusCode == http.StatusPartialContent || resp.Header.Get("Content-Range") != "" {
		return nil, errors.New("msgraph: refusing unsolicited partial content")
	}
	if resp.StatusCode == http.StatusNotModified && graph {
		return meta, ErrNotModified
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if resp.StatusCode >= 300 && resp.StatusCode <= 399 {
			return nil, &redirectFailure{cause: errUnsafeRedirect}
		}
		if !graph {
			return nil, fmt.Errorf("msgraph: content download failed: status %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		if err != nil {
			return nil, &downloadFailure{stage: "read error response", cause: err}
		}
		return nil, parseAPIError(resp.StatusCode, resp.Header, body)
	}
	reader := io.Reader(resp.Body)
	if c.maxResponseBytes > 0 {
		reader = io.LimitReader(reader, c.maxResponseBytes)
	}
	if _, err := io.Copy(out, reader); err != nil {
		return meta, &downloadFailure{stage: "stream content", cause: err}
	}
	if c.maxResponseBytes > 0 {
		var extra [1]byte
		n, err := io.ReadFull(resp.Body, extra[:])
		if n > 0 {
			return meta, ErrResponseTooLarge
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return meta, &downloadFailure{stage: "finish content", cause: err}
		}
	}
	return meta, nil
}

type downloadFailure struct {
	stage string
	cause error
}

func (e *downloadFailure) Error() string { return "msgraph: failed to " + e.stage }
func (e *downloadFailure) Unwrap() error { return e.cause }

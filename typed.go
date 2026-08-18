package msgraph

import (
	"context"
	"net/http"
)

// The package-level generic helpers are the type-safe front door to the
// client: the result type is named once at the call site and inferred
// everywhere after, instead of being smuggled through an `any` out-parameter
// that only fails at run time when it does not match the payload.
//
//	message, _, err := msgraph.Get[Message](ctx, client, "/me/messages/"+id, msgraph.Params{})
//
// The [Client] methods remain available for callers that need to decode into
// an existing value, stream into an io.Writer, or ignore the body.

// Get sends a GET and decodes the response into T.
func Get[T any](ctx context.Context, client *Client, path string, params Params) (T, *Response, error) {
	return Do[T](ctx, client, Request{Method: http.MethodGet, URL: path, Params: params})
}

// Post sends a POST with body and decodes the response into T. Use
// [PostNoContent] for endpoints such as /sendMail that answer 202 with no body.
func Post[T any](ctx context.Context, client *Client, path string, params Params, body any) (T, *Response, error) {
	return Do[T](ctx, client, Request{Method: http.MethodPost, URL: path, Params: params, Body: body})
}

// Patch sends a PATCH with body and decodes the response into T.
func Patch[T any](ctx context.Context, client *Client, path string, params Params, body any) (T, *Response, error) {
	return Do[T](ctx, client, Request{Method: http.MethodPatch, URL: path, Params: params, Body: body})
}

// PostNoContent sends a POST and discards any response body.
func PostNoContent(ctx context.Context, client *Client, path string, params Params, body any) (*Response, error) {
	return client.Do(ctx, Request{Method: http.MethodPost, URL: path, Params: params, Body: body}, nil)
}

// Delete sends a DELETE and discards any response body.
func Delete(ctx context.Context, client *Client, path string, params Params) (*Response, error) {
	return client.Do(ctx, Request{Method: http.MethodDelete, URL: path, Params: params}, nil)
}

// Do sends req and decodes the response into T. On error the returned T is the
// zero value.
func Do[T any](ctx context.Context, client *Client, req Request) (T, *Response, error) {
	var out T
	resp, err := client.Do(ctx, req, &out)
	if err != nil {
		var zero T
		return zero, resp, err
	}
	return out, resp, nil
}

// Collect drains a Graph collection into a slice. It is the eager counterpart
// to [Items]; prefer the iterator for large collections.
func Collect[T any](ctx context.Context, client *Client, path string, params Params, opts ...PageOption) ([]T, error) {
	var items []T
	for item, err := range Items[T](ctx, client, path, params, opts...) {
		if err != nil {
			return items, err
		}
		items = append(items, item)
	}
	return items, nil
}

// First returns the first item of a collection. found reports whether the
// collection had any items, distinguishing "no matches" from a zero-valued
// first item.
func First[T any](ctx context.Context, client *Client, path string, params Params) (value T, found bool, err error) {
	if params.Top == 0 {
		params.Top = 1
	}
	for item, itemErr := range Items[T](ctx, client, path, params) {
		if itemErr != nil {
			var zero T
			return zero, false, itemErr
		}
		return item, true, nil
	}
	var zero T
	return zero, false, nil
}

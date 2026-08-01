package msgraph

import (
	"context"
	"iter"
)

// Pages returns an iterator over Graph collection pages.
func Pages[T any](ctx context.Context, client *Client, path string, params Params) iter.Seq2[Page[T], error] {
	return func(yield func(Page[T], error) bool) {
		next := path
		nextParams := params
		for next != "" {
			var page Page[T]
			if _, err := client.Get(ctx, next, nextParams, &page); err != nil {
				_ = yield(Page[T]{}, err)
				return
			}
			if !yield(page, nil) {
				return
			}
			next = page.NextLink
			nextParams = Params{}
		}
	}
}

// Items returns an iterator over every item in a Graph collection.
func Items[T any](ctx context.Context, client *Client, path string, params Params) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for page, err := range Pages[T](ctx, client, path, params) {
			if err != nil {
				var zero T
				_ = yield(zero, err)
				return
			}
			for _, item := range page.Value {
				if !yield(item, nil) {
					return
				}
			}
		}
	}
}

package msgraph

import (
	"context"
	"errors"
	"fmt"
	"iter"
)

// Pagination errors returned by [Pages] and [Items].
var (
	ErrPageCycle        = errors.New("msgraph: repeated nextLink")
	ErrMaxPagesExceeded = errors.New("msgraph: max pages exceeded")
)

type pageConfig struct {
	maxPages int
}

// PageOption configures collection iteration.
type PageOption func(*pageConfig)

// WithMaxPages stops iteration after max pages. A non-positive maxPages leaves page
// count unlimited, though repeated nextLink values are still rejected.
func WithMaxPages(maxPages int) PageOption {
	return func(c *pageConfig) {
		c.maxPages = maxPages
	}
}

// Pages returns an iterator over Graph collection pages.
func Pages[T any](ctx context.Context, client *Client, path string, params Params, opts ...PageOption) iter.Seq2[Page[T], error] {
	return func(yield func(Page[T], error) bool) {
		cfg := pageConfig{}
		for _, opt := range opts {
			opt(&cfg)
		}
		next := path
		nextParams := params
		seen := map[string]bool{}
		pages := 0
		for next != "" {
			if seen[next] {
				_ = yield(Page[T]{}, fmt.Errorf("%w: %s", ErrPageCycle, next))
				return
			}
			if cfg.maxPages > 0 && pages >= cfg.maxPages {
				_ = yield(Page[T]{}, fmt.Errorf("%w: %d", ErrMaxPagesExceeded, cfg.maxPages))
				return
			}
			seen[next] = true
			pages++
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
func Items[T any](ctx context.Context, client *Client, path string, params Params, opts ...PageOption) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for page, err := range Pages[T](ctx, client, path, params, opts...) {
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

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

// DefaultMaxPages bounds a traversal that does not set its own limit.
//
// Detecting a repeated nextLink only catches a server that loops back on
// itself. One that returns a fresh link every time is followed forever, and
// the set of seen links grows without bound while it happens. At the largest
// page size Graph serves this still allows several million items, far past any
// real collection.
const DefaultMaxPages = 10_000

type pageConfig struct {
	maxPages int
}

// PageOption configures collection iteration.
type PageOption func(*pageConfig)

// WithMaxPages stops iteration after max pages, replacing [DefaultMaxPages]. A
// non-positive value removes the bound entirely, which is only safe when the
// server is trusted to terminate.
func WithMaxPages(maxPages int) PageOption {
	return func(c *pageConfig) {
		c.maxPages = maxPages
	}
}

// Pages returns an iterator over Graph collection pages.
func Pages[T any](ctx context.Context, client *Client, path string, params Params, opts ...PageOption) iter.Seq2[Page[T], error] {
	return func(yield func(Page[T], error) bool) {
		cfg := pageConfig{maxPages: DefaultMaxPages}
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

// Delta drains a Microsoft Graph delta query and returns the changed items
// together with the deltaLink from the final page.
//
// Pass a resource path such as "/me/mailFolders/inbox/messages/delta" for the
// initial full sync, then pass the returned deltaLink back on later calls to
// receive only what changed. Unlike [Items], delta results have to be drained
// eagerly: the deltaLink only arrives on the last page, and skipping it would
// lose the sync position.
func Delta[T any](
	ctx context.Context,
	client *Client,
	path string,
	params Params,
	opts ...PageOption,
) (items []T, deltaLink string, err error) {
	for page, pageErr := range Pages[T](ctx, client, path, params, opts...) {
		if pageErr != nil {
			return items, deltaLink, pageErr
		}
		items = append(items, page.Value...)
		if page.DeltaLink != "" {
			deltaLink = page.DeltaLink
		}
	}
	return items, deltaLink, nil
}

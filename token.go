package msgraph

import "context"

// TokenSource returns a Microsoft Graph access token. Implementations should
// refresh tokens before returning expired credentials.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// TokenSourceFunc adapts a function into a TokenSource.
type TokenSourceFunc func(ctx context.Context) (string, error)

// Token returns fn(ctx).
func (fn TokenSourceFunc) Token(ctx context.Context) (string, error) {
	return fn(ctx)
}

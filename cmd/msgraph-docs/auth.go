package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	msauth "github.com/albertocavalcante/msauth-go"
	"github.com/albertocavalcante/msauth-go/cache"
)

// Staging keeps an interactive wrong-account login from changing another
// tool's shared token cache. No secret is included in ordinary errors.
type stagedStore struct {
	base    cache.Store
	mu      sync.Mutex
	pending map[string][]byte
}

func (s *stagedStore) Load(ctx context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value, ok := s.pending[key]; ok {
		return slices.Clone(value), nil
	}
	return s.base.Load(ctx, key)
}
func (s *stagedStore) Save(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[key] = slices.Clone(value)
	return nil
}
func (s *stagedStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[key] = nil
	return nil
}
func (s *stagedStore) commit(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, value := range s.pending {
		var err error
		if value == nil {
			err = s.base.Delete(ctx, key)
		} else {
			err = s.base.Save(ctx, key, value)
		}
		if err != nil {
			return &safeFailure{message: "could not publish Microsoft sign-in cache", cause: err}
		}
	}
	return nil
}

func loginSafely(ctx context.Context, expected string, base cache.Store, signIn func(context.Context, cache.Store) (string, error)) (string, error) {
	staging := &stagedStore{base: base, pending: map[string][]byte{}}
	username, err := signIn(ctx, staging)
	if err != nil {
		return "", &safeFailure{message: "Microsoft sign-in did not complete", cause: err}
	}
	if expected == "" || !strings.EqualFold(username, expected) {
		return "", errors.New("signed-in account differs from the configured account; shared credentials were not changed")
	}
	if err := staging.commit(ctx); err != nil {
		return "", err
	}
	return username, nil
}

type safeFailure struct {
	message string
	cause   error
}

func (e *safeFailure) Error() string { return e.message }
func (e *safeFailure) Unwrap() error { return e.cause }

func authenticationError(err error, scope string) error {
	if errors.Is(err, msauth.ErrLoginRequired) {
		write := ""
		if scope == "Files.ReadWrite" {
			write = "--write "
		}
		return fmt.Errorf("required scope %s is unavailable; run msgraph-docs --account USER %slogin", scope, write)
	}
	return &safeFailure{message: "Microsoft authentication failed; existing consent was not changed", cause: err}
}

package main

import (
	"bytes"
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
	base     cache.Store
	mu       sync.Mutex
	pending  map[string][]byte
	original map[string][]byte
}

func (s *stagedStore) Load(ctx context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value, ok := s.pending[key]; ok {
		return slices.Clone(value), nil
	}
	return s.capture(ctx, key)
}
func (s *stagedStore) capture(ctx context.Context, key string) ([]byte, error) {
	if value, ok := s.original[key]; ok {
		return slices.Clone(value), nil
	}
	value, err := s.base.Load(ctx, key)
	if err != nil {
		return nil, err
	}
	s.original[key] = slices.Clone(value)
	return slices.Clone(value), nil
}
func (s *stagedStore) Save(ctx context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.capture(ctx, key); err != nil {
		return err
	}
	s.pending[key] = slices.Clone(value)
	return nil
}
func (s *stagedStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.capture(ctx, key); err != nil {
		return err
	}
	s.pending[key] = nil
	return nil
}
func (s *stagedStore) commit(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Check the whole read set before publishing any staged value.
	for key, original := range s.original {
		current, err := s.base.Load(ctx, key)
		if err != nil {
			return &safeFailure{message: "could not verify Microsoft sign-in cache", cause: err}
		}
		if !bytes.Equal(current, original) {
			return errors.New("document credential cache changed during sign-in; staged credentials were discarded")
		}
	}
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
	staging := &stagedStore{base: base, pending: map[string][]byte{}, original: map[string][]byte{}}
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

// A document-specific keychain keeps this CLI from writing to Outlook's shared
// credential store. Existing login data is a read-only fallback, not a target.
type isolatedStore struct {
	primary cache.Store
	seed    cache.Store
}

var deletedCache = []byte("msgraph-docs:deleted:v1")

func (s *isolatedStore) Load(ctx context.Context, key string) ([]byte, error) {
	value, err := s.primary.Load(ctx, key)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(value, deletedCache) {
		return nil, nil
	}
	if value != nil {
		return value, nil
	}
	return s.seed.Load(ctx, key)
}
func (s *isolatedStore) Save(ctx context.Context, key string, value []byte) error {
	return s.primary.Save(ctx, key, value)
}
func (s *isolatedStore) Delete(ctx context.Context, key string) error {
	return s.primary.Save(ctx, key, deletedCache)
}

type safeFailure struct {
	message string
	cause   error
}

func boundAuthenticator(cfg msauth.Config, store cache.Store, ref msauth.AccountRef) (*msauth.Authenticator, error) {
	// Discovery uses the username; the pinned SDK requires an exclusive exact
	// account reference once discovery is complete.
	cfg.Account = ""
	return msauth.NewForAccount(cfg, store, ref)
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

package msgraph

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientPersistentHistoryMutationCannotChangeOrigin(t *testing.T) {
	var destinationHits atomic.Int32
	var bearerForwarded atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationHits.Add(1)
		bearerForwarded.Store(r.Header.Get("Authorization") == "Bearer fixture-token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/middle", http.StatusFound)
		case "/middle":
			http.Redirect(w, r, destination.URL+"/finish", http.StatusFound)
		default:
			t.Errorf("unexpected origin path %s", r.URL.Path)
		}
	}))
	defer origin.Close()
	forged, err := url.Parse(destination.URL + "/forged-history")
	if err != nil {
		t.Fatal(err)
	}
	caller := &http.Client{CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) == 1 {
			via[0].URL = forged
		}
		return nil
	}}
	client, err := New(TokenSourceFunc(func(context.Context) (string, error) { return "fixture-token", nil }), WithBaseURL(origin.URL), WithHTTPClient(caller), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(t.Context(), "/start", Params{}, nil)
	if !errors.Is(err, ErrUntrustedHost) || destinationHits.Load() != 0 {
		t.Fatalf("redirect policy bypass: err=%v destinationHits=%d fakeBearerForwarded=%v", err, destinationHits.Load(), bearerForwarded.Load())
	}
}

func TestClientRedirectCannotReplaceOriginBinding(t *testing.T) {
	for _, change := range []string{"request", "history", "another-request"} {
		t.Run(change, func(t *testing.T) {
			var hits atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "/finish", http.StatusFound)
					return
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer origin.Close()
			var otherContext context.Context
			caller := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
				switch change {
				case "request":
					*req = *req.WithContext(context.Background())
				case "history":
					*via[0] = *via[0].WithContext(context.Background())
				case "another-request":
					*req = *req.WithContext(otherContext)
				}
				return nil
			}}
			client, err := New(staticToken("fixture-token"), WithBaseURL(origin.URL), WithHTTPClient(caller), WithMaxRetries(0))
			if err != nil {
				t.Fatal(err)
			}
			other, err := client.newHTTPRequest(t.Context(), http.MethodGet, Request{URL: "/different"}, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			otherContext = other.Context()
			_, err = client.Get(t.Context(), "/start", Params{}, nil)
			if !errors.Is(err, ErrUntrustedHost) || !errors.Is(err, ErrRedirectRefused) || hits.Load() != 1 {
				t.Errorf("binding replacement accepted: error=%v hits=%d", err, hits.Load())
			}
		})
	}
}

func TestClientConcurrentRequestsKeepIndependentOriginBindings(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var arrivals atomic.Int32
	release := make(chan struct{})
	newOrigin := func(label string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/start" {
				if arrivals.Add(1) == 2 {
					close(release)
				}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				http.Redirect(w, r, "/finish", http.StatusFound)
				return
			}
			_, _ = w.Write([]byte(`{"id":"` + label + `"}`))
		}))
	}
	first, second := newOrigin("first"), newOrigin("second")
	defer first.Close()
	defer second.Close()
	client, err := New(staticToken("fixture-token"), WithBaseURL(first.URL), WithAllowedHosts(second.URL), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, item := range []struct{ origin, want string }{{first.URL, "first"}, {second.URL, "second"}} {
		go func() {
			var result struct{ ID string }
			_, err := client.Get(ctx, item.origin+"/start", Params{}, &result)
			if err == nil && result.ID != item.want {
				err = errors.New("concurrent request returned another origin's result")
			}
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

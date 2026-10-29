package msgraph

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientRejectsRedirectToDifferentPort(t *testing.T) {
	var destinationHits atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destinationHits.Add(1)
		_, _ = w.Write([]byte(`{"id":"wrong-origin"}`))
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/next?cursor=private-fixture", http.StatusFound)
	}))
	defer origin.Close()
	client, err := New(staticToken("fixture-token"), WithBaseURL(origin.URL), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(t.Context(), "/start", Params{}, nil)
	if !errors.Is(err, ErrUntrustedHost) {
		t.Errorf("error = %v, want ErrUntrustedHost", err)
	}
	if got := destinationHits.Load(); got != 0 {
		t.Errorf("destination received %d forbidden requests", got)
	}
}

func TestClientValidatesDestinationBeforeToken(t *testing.T) {
	var tokenCalls atomic.Int32
	client, err := New(TokenSourceFunc(func(context.Context) (string, error) {
		tokenCalls.Add(1)
		return "fixture-token", nil
	}), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(t.Context(), "https://elsewhere.invalid/next?cursor=private-fixture", Params{}, nil)
	if !errors.Is(err, ErrUntrustedHost) {
		t.Errorf("error = %v, want ErrUntrustedHost", err)
	}
	if got := tokenCalls.Load(); got != 0 {
		t.Errorf("token source called %d times before rejecting destination", got)
	}
}

func TestClientRejectsInvalidDestinationsBeforeToken(t *testing.T) {
	for _, target := range []string{
		"https://graph.microsoft.com/%ZZ?cursor=private-fixture",
		"https://username:private-fixture@graph.microsoft.com/v1.0/me",
		"https:graph.microsoft.com/private-fixture",
		"https://graph.microsoft.com:99999/me?cursor=private-fixture",
		"https://graph.microsoft.com:/me?cursor=private-fixture",
		"https://graph.microsoft.com/me#private-fixture",
		"//elsewhere.invalid/me?cursor=private-fixture",
		"/me#private-fixture",
	} {
		t.Run(target, func(t *testing.T) {
			var calls atomic.Int32
			client, err := New(TokenSourceFunc(func(context.Context) (string, error) {
				calls.Add(1)
				return "fixture-token", nil
			}), WithMaxRetries(0))
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Get(t.Context(), target, Params{}, nil)
			if err == nil || strings.Contains(err.Error(), "private-fixture") {
				t.Errorf("error = %v, want redacted rejection", err)
			}
			if got := calls.Load(); got != 0 {
				t.Errorf("token called %d times", got)
			}
		})
	}
}

func TestOriginComparison(t *testing.T) {
	for _, test := range []struct {
		name   string
		origin string
		target string
		allow  bool
	}{
		{"same", "https://graph.microsoft.com/v1.0", "https://graph.microsoft.com/next", true},
		{"case", "https://GRAPH.microsoft.COM/v1.0", "https://graph.microsoft.com/next", true},
		{"default HTTPS port", "https://graph.microsoft.com", "https://graph.microsoft.com:443/next", true},
		{"default HTTP port", "http://graph.microsoft.com:80", "http://graph.microsoft.com/next", true},
		{"numeric port", "https://graph.microsoft.com:00443", "https://graph.microsoft.com:443/next", true},
		{"IPv6", "http://[::1]:8080", "http://[::1]:8080/next", true},
		{"port", "https://graph.microsoft.com", "https://graph.microsoft.com:444/next", false},
		{"downgrade", "https://graph.microsoft.com", "http://graph.microsoft.com/next", false},
		{"upgrade", "http://graph.microsoft.com", "https://graph.microsoft.com/next", false},
		{"subdomain", "https://graph.microsoft.com", "https://sub.graph.microsoft.com/next", false},
		{"hostname", "https://graph.microsoft.com", "https://elsewhere.invalid/next", false},
		{"empty port", "https://graph.microsoft.com", "https://graph.microsoft.com:/next", false},
		{"userinfo", "https://graph.microsoft.com", "https://name:fixture@graph.microsoft.com/next", false},
		{"opaque", "https://graph.microsoft.com", "https:graph.microsoft.com", false},
		{"fragment", "https://graph.microsoft.com", "https://graph.microsoft.com/next#fragment", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			origin, err := url.Parse(test.origin)
			if err != nil {
				t.Fatal(err)
			}
			target, err := url.Parse(test.target)
			if err != nil {
				t.Fatal(err)
			}
			if got := sameOrigin(origin, target); got != test.allow {
				t.Errorf("sameOrigin = %v, want %v", got, test.allow)
			}
		})
	}
}

func TestClientRejectsInvalidBaseURL(t *testing.T) {
	for _, base := range []string{"", "/v1.0", "ftp://graph.microsoft.com", "https://user:fixture@graph.microsoft.com", "https://graph.microsoft.com:/v1.0", "https://graph.microsoft.com/v1.0?fixture=secret", "https://graph.microsoft.com/v1.0#secret"} {
		if _, err := New(staticToken("fixture"), WithBaseURL(base)); err == nil {
			t.Errorf("accepted base %q", base)
		}
	}
}

func TestClientDirectAllowedHostDoesNotAuthorizeRedirect(t *testing.T) {
	var targetHits atomic.Int32
	var authSeen atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		authSeen.Store(r.Header.Get("Authorization") == "Bearer fixture-token")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/next?cursor=private-fixture", http.StatusFound)
	}))
	defer origin.Close()
	client, err := New(staticToken("fixture-token"), WithBaseURL(origin.URL), WithAllowedHosts(target.URL), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(t.Context(), "/redirect", Params{}, nil)
	if !errors.Is(err, ErrUntrustedHost) || !errors.Is(err, ErrRedirectRefused) {
		t.Fatalf("redirect error = %v", err)
	}
	if strings.Contains(err.Error(), "private-fixture") || targetHits.Load() != 0 {
		t.Fatalf("forbidden request sent or URL leaked: hits=%d error=%v", targetHits.Load(), err)
	}
	if _, err := client.Get(t.Context(), target.URL+"/direct", Params{}, nil); err != nil {
		t.Fatal(err)
	}
	if targetHits.Load() != 1 || !authSeen.Load() {
		t.Fatal("explicitly permitted direct request did not carry fixture token")
	}
}

func TestClientFollowsSameOriginRedirectVerbatim(t *testing.T) {
	const query = "$skiptoken=AQ8A~PQ%3d%3d&$deltatoken=LzxYcC9]C(o*!~x"
	var observed string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/next?"+query, http.StatusFound)
			return
		}
		observed = r.URL.RawQuery
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client, err := New(staticToken("fixture-token"), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(t.Context(), "/start", Params{}, nil); err != nil {
		t.Fatal(err)
	}
	if observed != query {
		t.Errorf("redirect rewrote query: %q", observed)
	}
}

func TestClientRejectsRedirectDowngrade(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer target.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/next", http.StatusFound)
	}))
	defer origin.Close()
	client, err := New(staticToken("fixture-token"), WithBaseURL(origin.URL), WithHTTPClient(origin.Client()), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(t.Context(), "/start", Params{}, nil)
	if !errors.Is(err, ErrUntrustedHost) || targetHits.Load() != 0 {
		t.Errorf("error=%v target hits=%d", err, targetHits.Load())
	}
}

func TestClientRedirectPolicyPreservesCallerClient(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/again?cursor=private-fixture", http.StatusFound)
	}))
	defer server.Close()
	callerError := errors.New("caller-private-fixture")
	var callbacks atomic.Int32
	original := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		callbacks.Add(1)
		return callerError
	}}
	var sleeps atomic.Int32
	client, err := New(staticToken("fixture-token"), WithBaseURL(server.URL), WithHTTPClient(original), WithSleeper(func(context.Context, time.Duration) error {
		sleeps.Add(1)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if client.httpClient == original || client.httpClient.Timeout != original.Timeout {
		t.Fatal("caller HTTP client was not copied with its timeout")
	}
	_, err = client.Get(t.Context(), "/start", Params{}, nil)
	if !errors.Is(err, callerError) || !errors.Is(err, ErrRedirectRefused) {
		t.Fatalf("error = %v, want caller error and ErrRedirectRefused", err)
	}
	if strings.Contains(err.Error(), "private-fixture") || sleeps.Load() != 0 || hits.Load() != 1 || callbacks.Load() != 1 {
		t.Fatalf("policy rejection was leaked/retried: error=%v sleeps=%d hits=%d callbacks=%d", err, sleeps.Load(), hits.Load(), callbacks.Load())
	}
	if err := original.CheckRedirect(nil, nil); !errors.Is(err, callerError) || callbacks.Load() != 2 {
		t.Fatal("original redirect callback changed")
	}
}

func TestClientRedirectCallbackCannotChangeDestinationOrMethod(t *testing.T) {
	for _, mutation := range []string{"destination", "method", "history", "host"} {
		t.Run(mutation, func(t *testing.T) {
			var targetHits atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				targetHits.Add(1)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer target.Close()
			var originHits atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				originHits.Add(1)
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "/again", http.StatusFound)
					return
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer origin.Close()
			caller := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if mutation == "host" {
					req.Host = "elsewhere.invalid"
					return nil
				}
				if mutation == "method" {
					req.Method = http.MethodPost
					return nil
				}
				newURL, err := url.Parse(target.URL + "/next")
				if err != nil {
					return err
				}
				req.URL = newURL
				if mutation == "history" {
					via[0].URL = newURL
				}
				return nil
			}}
			client, err := New(staticToken("fixture-token"), WithBaseURL(origin.URL), WithHTTPClient(caller), WithMaxRetries(0))
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Get(t.Context(), "/start", Params{}, nil)
			if !errors.Is(err, ErrRedirectRefused) || targetHits.Load() != 0 || originHits.Load() != 1 {
				t.Errorf("error=%v target hits=%d origin hits=%d", err, targetHits.Load(), originHits.Load())
			}
		})
	}
}

func TestClientRedirectLoopStopsWithoutRetry(t *testing.T) {
	var hits, sleeps atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/again?cursor=private-fixture", http.StatusFound)
	}))
	defer server.Close()
	client, err := New(staticToken("fixture-token"), WithBaseURL(server.URL), WithSleeper(func(context.Context, time.Duration) error {
		sleeps.Add(1)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(t.Context(), "/start", Params{}, nil)
	if !errors.Is(err, ErrRedirectRefused) || hits.Load() != defaultMaxRedirects || sleeps.Load() != 0 || strings.Contains(err.Error(), "private-fixture") {
		t.Errorf("error=%v hits=%d sleeps=%d", err, hits.Load(), sleeps.Load())
	}
}

func TestClientDoesNotRedirectUnsafeMethods(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete} {
		for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
			t.Run(fmt.Sprintf("%s/%d", method, status), func(t *testing.T) {
				var hits atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					http.Redirect(w, r, "/again", status)
				}))
				defer server.Close()
				client, err := New(staticToken("fixture-token"), WithBaseURL(server.URL), WithRetryUnsafeMethods(true), WithSleeper(func(context.Context, time.Duration) error { return nil }))
				if err != nil {
					t.Fatal(err)
				}
				_, err = client.Do(t.Context(), Request{Method: method, URL: "/start", RawBody: []byte("fixture")}, nil)
				if !errors.Is(err, ErrRedirectRefused) || hits.Load() != 1 {
					t.Errorf("error=%v hits=%d", err, hits.Load())
				}
			})
		}
	}
}

func TestClientPreservesUseLastResponse(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	defer server.Close()
	caller := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	client, err := New(staticToken("fixture-token"), WithBaseURL(server.URL), WithHTTPClient(caller))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(t.Context(), "/start", Params{}, nil)
	if !IsStatus(err, http.StatusFound) || errors.Is(err, ErrRedirectRefused) || hits.Load() != 1 {
		t.Errorf("error=%v hits=%d", err, hits.Load())
	}
}

func TestClientNormalizesDefaultPortBeforeToken(t *testing.T) {
	for _, test := range []struct{ base, target string }{
		{"https://graph.microsoft.com/v1.0", "https://graph.microsoft.com:443/v1.0/me"},
		{"http://graph.microsoft.com:80/v1.0", "http://graph.microsoft.com/v1.0/me"},
	} {
		client, err := New(staticToken("fixture-token"), WithBaseURL(test.base))
		if err != nil {
			t.Fatal(err)
		}
		req, err := client.newHTTPRequest(t.Context(), http.MethodGet, Request{URL: test.target}, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if req.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("default-port request did not receive token")
		}
	}
}

func TestBatchUsesSameOriginValidation(t *testing.T) {
	client, err := New(staticToken("fixture-token"))
	if err != nil {
		t.Fatal(err)
	}
	const query = "$skiptoken=opaque%3d%3d"
	got, err := client.normalizeBatchURL("https://graph.microsoft.com:443/v1.0/me?" + query)
	if err != nil || got != "/me?"+query {
		t.Fatalf("URL=%q error=%v", got, err)
	}
	for _, value := range []string{
		"https://graph.microsoft.com:444/me?cursor=private-fixture",
		"https://user:private-fixture@graph.microsoft.com/v1.0/me",
		"https://graph.microsoft.com/%ZZ?cursor=private-fixture",
		"//elsewhere.invalid/me?cursor=private-fixture",
		"/me#private-fixture",
	} {
		if _, err := client.normalizeBatchURL(value); err == nil || strings.Contains(err.Error(), "private-fixture") {
			t.Errorf("URL validation failed to reject/redact %q: %v", value, err)
		}
	}
}

func TestClientPreservesCustomRedirectCountLimit(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	defer server.Close()
	stop := errors.New("custom count limit")
	caller := &http.Client{CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) == 2 {
			return stop
		}
		return nil
	}}
	client, err := New(staticToken("fixture-token"), WithBaseURL(server.URL), WithHTTPClient(caller))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(t.Context(), "/start", Params{}, nil)
	if !errors.Is(err, stop) || !errors.Is(err, ErrRedirectRefused) || hits.Load() != 2 {
		t.Errorf("error=%v hits=%d", err, hits.Load())
	}
}

func TestClientPreservesCallerWithNoRedirectCallback(t *testing.T) {
	original := &http.Client{Timeout: 3 * time.Second}
	client, err := New(staticToken("fixture-token"), WithHTTPClient(original))
	if err != nil {
		t.Fatal(err)
	}
	if original.CheckRedirect != nil || client.httpClient == original || client.httpClient.Timeout != original.Timeout {
		t.Fatal("construction mutated caller-owned configuration")
	}
}

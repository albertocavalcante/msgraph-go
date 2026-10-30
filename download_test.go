package msgraph

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDownloadSignedURLHasNoCredentials(t *testing.T) {
	var contentCalls atomic.Int32
	content := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentCalls.Add(1)
		for _, name := range []string{"Authorization", "Cookie", "Referer"} {
			if r.Header.Get(name) != "" {
				t.Errorf("content received %s", name)
			}
		}
		if r.URL.RawQuery != "secret=signed-value" {
			t.Errorf("signed query changed: %q", r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, "%PDF-document")
	}))
	defer content.Close()
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer graph-secret" {
			t.Error("Graph bearer missing")
		}
		if r.URL.Query().Get("format") != "pdf" {
			t.Error("conversion query missing")
		}
		http.Redirect(w, r, content.URL+"/file?secret=signed-value", http.StatusFound)
	}))
	defer graph.Close()
	transport := content.Client()
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(content.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: "ambient", Value: "secret"}})
	transport.Jar = jar
	c, err := New(TokenSourceFunc(func(context.Context) (string, error) { return "graph-secret", nil }), WithBaseURL(graph.URL), WithHTTPClient(transport))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_, err = c.Download(t.Context(), "/me/drive/items/example/content", Params{Custom: url.Values{"format": {"pdf"}}}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "%PDF-document" || contentCalls.Load() != 1 {
		t.Fatalf("download = %q, calls=%d", out.String(), contentCalls.Load())
	}
}

func TestDownloadRejectsUnsafeLocationsAndSecondaryRedirect(t *testing.T) {
	var reached atomic.Int32
	content := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		http.Redirect(w, r, "https://example.invalid/leak", http.StatusFound)
	}))
	defer content.Close()
	for _, target := range []string{"http://example.invalid/signed?secret=hidden", "https://user:password@example.invalid/file", "https://example.invalid/file#secret", content.URL + "/file?secret=hidden"} {
		t.Run(target, func(t *testing.T) {
			graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target)
				w.WriteHeader(http.StatusFound)
			}))
			defer graph.Close()
			c, err := New(TokenSourceFunc(func(context.Context) (string, error) { return "secret", nil }), WithBaseURL(graph.URL), WithHTTPClient(content.Client()))
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Download(t.Context(), "/content", Params{}, io.Discard)
			if !errors.Is(err, ErrRedirectRefused) {
				t.Fatalf("expected redirect refusal, got %v", err)
			}
			if strings.Contains(err.Error(), "hidden") || strings.Contains(err.Error(), "password") {
				t.Fatal("error exposed signed URL")
			}
		})
	}
	if reached.Load() != 1 {
		t.Fatalf("unexpected content requests: %d", reached.Load())
	}
}

func TestDownloadLimitsAndGraphErrors(t *testing.T) {
	for _, body := range []string{"1234", "12345"} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
			defer server.Close()
			c, _ := New(TokenSourceFunc(func(context.Context) (string, error) { return "token", nil }), WithBaseURL(server.URL), WithMaxResponseBytes(4))
			var out bytes.Buffer
			_, err := c.Download(t.Context(), "/content", Params{}, &out)
			if (len(body) > 4) != errors.Is(err, ErrResponseTooLarge) {
				t.Fatalf("length %d: %v", len(body), err)
			}
			if out.Len() > 4 {
				t.Fatal("wrote beyond configured limit")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		_, _ = io.WriteString(w, `{"error":{"code":"accessDenied","message":"permission required"}}`)
	}))
	defer server.Close()
	c, _ := New(TokenSourceFunc(func(context.Context) (string, error) { return "token", nil }), WithBaseURL(server.URL))
	_, err := c.Download(t.Context(), "/content", Params{}, io.Discard)
	if !IsForbidden(err) || !HasCode(err, "accessDenied") {
		t.Fatalf("lost Graph error: %v", err)
	}
}

func TestDownloadValidatesBeforeToken(t *testing.T) {
	var calls int
	c, _ := New(TokenSourceFunc(func(context.Context) (string, error) { calls++; return "token", nil }))
	for _, tc := range []struct {
		path   string
		out    io.Writer
		params Params
	}{{"https://off-origin.invalid/file", io.Discard, Params{}}, {"/content", nil, Params{}}, {"/content", io.Discard, Params{Top: -1}}} {
		if _, err := c.Download(t.Context(), tc.path, tc.params, tc.out); err == nil {
			t.Error("invalid download accepted")
		}
	}
	if calls != 0 {
		t.Fatalf("token requested %d times", calls)
	}
}

func TestDownloadDoesNotRetryPartialStreams(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = io.WriteString(w, "payload") }))
	defer server.Close()
	c, _ := New(TokenSourceFunc(func(context.Context) (string, error) { return "token", nil }), WithBaseURL(server.URL))
	_, err := c.Download(t.Context(), "/content", Params{}, failingDownloadWriter{})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

type failingDownloadWriter struct{}

func (failingDownloadWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestDownloadPreservesStreamCauses(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, io.ErrUnexpectedEOF} {
		c, err := New(TokenSourceFunc(func(context.Context) (string, error) { return "token", nil }), WithHTTPClient(&http.Client{Transport: downloadRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(downloadErrorReader{cause})}, nil
		})}))
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Download(t.Context(), "/content", Params{}, io.Discard)
		if !errors.Is(err, cause) {
			t.Fatalf("lost %v: %v", cause, err)
		}
	}
}

func TestDownloadRefusesPartialContent(t *testing.T) {
	for _, signed := range []bool{false, true} {
		content := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.WriteString(w, "partial")
		}))
		graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if signed {
				http.Redirect(w, r, content.URL+"/file", http.StatusFound)
			} else {
				w.WriteHeader(http.StatusPartialContent)
				_, _ = io.WriteString(w, "partial")
			}
		}))
		c, _ := New(TokenSourceFunc(func(context.Context) (string, error) { return "token", nil }), WithBaseURL(graph.URL), WithHTTPClient(content.Client()))
		var out bytes.Buffer
		if _, err := c.Download(t.Context(), "/content", Params{}, &out); err == nil || out.Len() != 0 {
			t.Fatal("partial content accepted as a complete file")
		}
		graph.Close()
		content.Close()
	}
}

type downloadRoundTripFunc func(*http.Request) (*http.Response, error)

func (f downloadRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type downloadErrorReader struct{ cause error }

func (r downloadErrorReader) Read([]byte) (int, error) { return 0, r.cause }

func TestDownloadKeepsWriterCauseAndRejectsContentRange(t *testing.T) {
	cause := errors.New("writer failure with sensitive callback detail")
	c, _ := New(TokenSourceFunc(func(context.Context) (string, error) { return "token", nil }), WithHTTPClient(&http.Client{Transport: downloadRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("document"))}, nil
	})}))
	_, err := c.Download(t.Context(), "/content", Params{}, downloadCauseWriter{cause})
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("writer cause not preserved safely: %v", err)
	}
	c, _ = New(TokenSourceFunc(func(context.Context) (string, error) { return "token", nil }), WithHTTPClient(&http.Client{Transport: downloadRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Range": {"bytes 0-3/100"}}, Body: io.NopCloser(strings.NewReader("part"))}, nil
	})}))
	var out bytes.Buffer
	if _, err := c.Download(t.Context(), "/content", Params{}, &out); err == nil || out.Len() != 0 {
		t.Fatal("unsolicited Content-Range accepted")
	}
}

type downloadCauseWriter struct{ cause error }

func (w downloadCauseWriter) Write([]byte) (int, error) { return 0, w.cause }

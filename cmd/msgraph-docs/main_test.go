package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/albertocavalcante/msauth-go"
	"github.com/albertocavalcante/msauth-go/cache"
	msgraph "github.com/albertocavalcante/msgraph-go"
)

func TestUploadPreviewIsOfflineAndDoesNotPrintContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "document.docx")
	const content = "private document marker that must not appear in the plan"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run([]string{"upload", "--file", path, "--name", "review.docx"}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), content) || !strings.Contains(output.String(), `"apply": false`) || !strings.Contains(output.String(), `"sha256"`) {
		t.Fatalf("unsafe or incomplete plan: %s", output.String())
	}
}

func TestArgumentAndProfileValidation(t *testing.T) {
	for _, name := range []string{"../replace.docx", "file\x00.docx", "file\x01.docx", " trailing ", "a\\b.docx"} {
		if validName(name) {
			t.Errorf("accepted name %q", name)
		}
	}
	if _, err := parse([]string{"--account", "owner@example.test", "download", "--id", "../bad", "--out", "example"}, io.Discard); err == nil {
		t.Fatal("accepted traversal ID")
	}
	if _, err := parse([]string{"upload", "--file", "document", "--name", "review.docx", "--apply"}, io.Discard); err == nil {
		t.Fatal("upload apply did not require an account")
	}
	for _, raw := range []string{`{"account":"owner@example.test","token":"private"}`, `{"account":"owner@example.test"} {}`, `{"account":"owner@example.test"}` + strings.Repeat(" ", 16384) + `{}`} {
		path := filepath.Join(t.TempDir(), "profile.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readProfile(path); err == nil {
			t.Fatal("accepted invalid or oversized profile")
		}
	}
}

func fakeGraph(t *testing.T, handler http.HandlerFunc) *msgraph.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	graph, err := msgraph.New(msgraph.TokenSourceFunc(func(context.Context) (string, error) { return "test-token", nil }), msgraph.WithBaseURL(server.URL), msgraph.WithMaxResponseBytes(documentLimit))
	if err != nil {
		t.Fatal(err)
	}
	return graph
}

func TestUploadPreservesExistingAndRacingDestinations(t *testing.T) {
	for _, exists := range []bool{true, false} {
		t.Run(map[bool]string{true: "existing", false: "race"}[exists], func(t *testing.T) {
			puts := 0
			graph := fakeGraph(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					if exists {
						_, _ = io.WriteString(w, `{"id":"existing"}`)
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
					return
				}
				puts++
				if r.Method != http.MethodPut || r.URL.Query().Get("@microsoft.graph.conflictBehavior") != "fail" || r.Header.Get("If-None-Match") != "*" {
					t.Error("missing server-side no-overwrite guard")
				}
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"error":{"code":"nameAlreadyExists","message":"signed-secret-must-not-be-printed"}}`)
			})
			var out bytes.Buffer
			err := execute(t.Context(), command{action: "upload", name: "new.docx"}, graph, "owner@example.test", nil, []byte("new content"), &out)
			if err == nil || out.Len() != 0 {
				t.Fatal("conflicting upload reported success")
			}
			if strings.Contains(err.Error(), "signed-secret") {
				t.Fatal("Graph message leaked")
			}
			if exists && puts != 0 || !exists && puts != 1 {
				t.Fatalf("unexpected mutating requests: %d", puts)
			}
		})
	}
}

func TestDownloadFailureAndExistingFileArePreserved(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusPartialContent} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			graph := fakeGraph(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "document")
			})
			directory := t.TempDir()
			destination := filepath.Join(directory, "document.pdf")
			if status == http.StatusOK {
				if err := os.WriteFile(destination, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := downloadFile(t.Context(), graph, "/content", msgraph.Params{}, destination); err == nil {
				t.Fatal("unsafe publication succeeded")
			}
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			if status == http.StatusPartialContent {
				if len(entries) != 0 {
					t.Fatal("partial file or staging file survived")
				}
			} else {
				data, err := os.ReadFile(destination)
				if err != nil || string(data) != "original" || len(entries) != 1 {
					t.Fatal("existing local document changed")
				}
			}
		})
	}
}

func TestLoginCachesOnlyTheExpectedAccount(t *testing.T) {
	for _, match := range []bool{false, true} {
		base := &memoryStore{data: map[string][]byte{"cache": []byte("original")}}
		signin := func(ctx context.Context, staging cache.Store) (string, error) {
			if err := staging.Save(ctx, "cache", []byte("new-token-data")); err != nil {
				return "", err
			}
			if match {
				return "OWNER@example.test", nil
			}
			return "other@example.test", nil
		}
		_, err := loginSafely(t.Context(), "owner@example.test", base, signin)
		if match {
			if err != nil || base.saves != 1 || string(base.data["cache"]) != "new-token-data" {
				t.Fatal("expected account not published")
			}
		} else if err == nil || base.saves != 0 || string(base.data["cache"]) != "original" {
			t.Fatal("wrong account changed shared cache")
		}
	}
}

func TestAuthenticationErrorsKeepTheirMeaning(t *testing.T) {
	err := authenticationError(context.Canceled, "Files.ReadWrite")
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "login") {
		t.Fatal("cancellation reported as missing consent")
	}
	err = authenticationError(msauth.ErrLoginRequired, "Files.ReadWrite")
	if !strings.Contains(err.Error(), "--write login") {
		t.Fatal("missing concrete consent remedy")
	}
	base := &memoryStore{data: map[string][]byte{}}
	_, err = loginSafely(t.Context(), "owner@example.test", base, func(ctx context.Context, store cache.Store) (string, error) {
		_ = store.Save(ctx, "cache", []byte("staged"))
		return "", context.DeadlineExceeded
	})
	if !errors.Is(err, context.DeadlineExceeded) || base.saves != 0 {
		t.Fatal("failed login published credentials or lost cause")
	}
}

type memoryStore struct {
	data  map[string][]byte
	saves int
}

func (s *memoryStore) Load(_ context.Context, key string) ([]byte, error) {
	return bytes.Clone(s.data[key]), nil
}
func (s *memoryStore) Save(_ context.Context, key string, data []byte) error {
	s.saves++
	s.data[key] = bytes.Clone(data)
	return nil
}
func (s *memoryStore) Delete(_ context.Context, key string) error { delete(s.data, key); return nil }

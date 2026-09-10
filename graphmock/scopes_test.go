package graphmock_test

import (
	"context"
	"net/http"
	"testing"

	msgraph "github.com/albertocavalcante/msgraph-go"
	"github.com/albertocavalcante/msgraph-go/graphmock"
)

// TestRequireScopesRefusesUngrantedResource reproduces a bug that reached a
// real mailbox: a client holding only the mail permissions read mailbox
// settings and was refused, because a token covers the scopes requested rather
// than everything consented to. Every path below was answered identically by
// the old fake, which is why the suite stayed green while the CLI failed.
func TestRequireScopesRefusesUngrantedResource(t *testing.T) {
	mailOnly := []string{msgraph.ScopeUserRead, msgraph.ScopeMailReadWrite, msgraph.ScopeMailSend}

	for _, tc := range []struct {
		name    string
		granted []string
		method  string
		path    string
		want    int
	}{
		{"mail token reads mail", mailOnly, http.MethodGet, "/me/messages", http.StatusOK},
		{"mail token reads settings", mailOnly, http.MethodGet, "/me/mailboxSettings", http.StatusForbidden},
		{"mail token writes settings", mailOnly, http.MethodPatch, "/me/mailboxSettings", http.StatusForbidden},
		{
			"settings token reads settings",
			append(append([]string{}, mailOnly...), msgraph.ScopeMailboxSettingsRead),
			http.MethodGet, "/me/mailboxSettings", http.StatusOK,
		},
		{
			// The read-write permission stands in for the read one. Treating
			// these as unrelated strings would refuse a caller that holds
			// strictly more than it needs.
			"read-write token reads settings",
			append(append([]string{}, mailOnly...), msgraph.ScopeMailboxSettingsReadWrite),
			http.MethodGet, "/me/mailboxSettings", http.StatusOK,
		},
		{
			// Casing differs between what is requested and what the service
			// echoes back; a case difference is not a missing permission.
			"casing does not deny",
			[]string{"mailboxsettings.read"},
			http.MethodGet, "/me/mailboxSettings", http.StatusOK,
		},
		{
			"read token cannot write settings",
			append(append([]string{}, mailOnly...), msgraph.ScopeMailboxSettingsRead),
			http.MethodPatch, "/me/mailboxSettings", http.StatusForbidden,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := graphmock.New(t, graphmock.WithConstraints(graphmock.RequireScopes(tc.granted...)))
			ok := func(*graphmock.Request) graphmock.Response {
				return graphmock.Item(map[string]any{"language": "en-US"})
			}
			server.Handle("GET /me/messages", ok)
			server.Handle("GET /me/mailboxSettings", ok)
			server.Handle("PATCH /me/mailboxSettings", ok)

			req, err := http.NewRequestWithContext(context.Background(), tc.method, server.URL()+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.want {
				t.Errorf("%s %s = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
			}
		})
	}
}

// TestRequireScopesAllowsUnknownPaths keeps the constraint from failing tests
// for routes the permission table does not describe. An unknown path makes no
// claim about what it needs, and guessing would be worse than allowing.
func TestRequireScopesAllowsUnknownPaths(t *testing.T) {
	server := graphmock.New(t, graphmock.WithConstraints(graphmock.RequireScopes()))
	server.Handle("GET /me/somethingNotInTheTable", func(*graphmock.Request) graphmock.Response {
		return graphmock.Item(map[string]any{"ok": true})
	})

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		server.URL()+"/me/somethingNotInTheTable", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("unknown path = %d, want 200", resp.StatusCode)
	}
}

package msgraph

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRouteTemplate(t *testing.T) {
	// Paths go through resolveURL, as a real request does, so each case sees
	// the encoding production produces.
	cases := []struct {
		name string
		path string
		want string
	}{
		{"me", "/me", "/v1.0/me"},
		{"message id", "/me/messages/AAMkADAwATM0MDAAMS1iNTcwLWI2NTEtMDACLTAwCgBGAAA=", "/v1.0/me/messages/{id}"},
		{"folder and message ids", "/me/mailFolders/AQMkAGI2/messages/AAMkAGI2TG93", "/v1.0/me/mailFolders/{id}/messages/{id}"},
		{"well-known folder", "/me/mailFolders/inbox/messages", "/v1.0/me/mailFolders/inbox/messages"},
		{"well-known folder casing", "/me/MailFolders/Inbox/Messages", "/v1.0/me/mailFolders/inbox/messages"},
		{"user principal name", "/users/someone@example.com/messages", "/v1.0/users/{id}/messages"},
		{"escaped user principal name", "/users/someone%40example.com", "/v1.0/users/{id}"},
		{"user key in parentheses", "/users('someone@example.com')/events", "/v1.0/users({id})/events"},
		{"unknown name with parentheses", "/reports/getEmailActivityUserDetail(period='D7')", "/v1.0/{id}"},
		{"unbalanced parenthesis", "/users(someone", "/v1.0/{id}"},
		{"batch", "/$batch", "/v1.0/$batch"},
		{"delta", "/me/mailFolders/inbox/messages/delta", "/v1.0/me/mailFolders/inbox/messages/delta"},
		{"delta function", "/me/drive/root/delta()", "/v1.0/me/drive/root/delta()"},
		{"delta function with token argument", "/me/drive/root/delta(token='abc')", "/v1.0/me/drive/root/delta({id})"},
		{"qualified function", "/me/drive/root/microsoft.graph.delta()", "/v1.0/me/drive/root/microsoft.graph.delta()"},
		{"type cast", "/me/memberOf/microsoft.graph.group", "/v1.0/me/memberOf/microsoft.graph.group"},
		{"fake qualified name", "/me/microsoft.graph.someone@example.com", "/v1.0/me/{id}"},
		{"key equals value", "/sites/id=1234", "/v1.0/sites/{id}"},
		{"path-based addressing collapses", "/me/drive/root:/Reports/2026/Budget.xlsx:/content", "/v1.0/me/drive/{id}/content"},
		{"site composite id", "/sites/contoso.sharepoint.com,1111,2222/lists", "/v1.0/sites/{id}/lists"},
		{"numeric segment", "/me/events/12345", "/v1.0/me/events/{id}"},
		{"chat message", "/chats/19:abc@thread.v2/messages/1700000000000", "/v1.0/chats/{id}/messages/{id}"},
		{"value", "/me/messages/AAMk/$value", "/v1.0/me/messages/{id}/$value"},
		{"unknown fixed-looking word", "/me/secretProjectName", "/v1.0/me/{id}"},
		// Path-based addressing is one identifier, whatever words it contains.
		{"addressed path with fixed words", "/me/drive/root:/Projects/Archive/Contacts/Budget.xlsx:/content", "/v1.0/me/drive/{id}/content"},
		{"addressed path below an item", "/me/drive/items/01ABC:/sub/inbox/drafts:/content", "/v1.0/me/drive/items/{id}/content"},
		{"unterminated addressed path", "/me/drive/root:/Projects/messages/events", "/v1.0/me/drive/{id}"},
		{"site by path", "/sites/contoso.sharepoint.com:/sites/Team:/lists", "/v1.0/sites/{id}/lists"},
		// Qualified names come from an explicit list, not a pattern.
		{"unknown qualified name", "/me/microsoft.graph.JohnSmithSecret", "/v1.0/me/{id}"},
		{"very long qualified name", "/me/microsoft.graph." + strings.Repeat("a", 5000), "/v1.0/me/{id}"},
		{"cast after a collection", "/users/AAA/transitiveMemberOf/microsoft.graph.group", "/v1.0/users/{id}/transitiveMemberOf/microsoft.graph.group"},
		// After a collection the next segment is an identifier unless it is
		// one of that collection's known children.
		{"list title", "/sites/root/lists/Tasks/items", "/v1.0/sites/root/lists/{id}/items"},
		{"list title equal to a collection", "/sites/SITE/lists/Contacts/items/7", "/v1.0/sites/{id}/lists/{id}/items/{id}"},
		{"todo list", "/me/todo/lists/AAA/tasks/BBB", "/v1.0/me/todo/lists/{id}/tasks/{id}"},
		{"todo delta", "/me/todo/lists/delta", "/v1.0/me/todo/lists/delta"},
		{"calendar named like a collection", "/me/calendars/events/events", "/v1.0/me/calendars/{id}/events"},
		{"contact folder", "/me/contactFolders/AAA/contacts/delta", "/v1.0/me/contactFolders/{id}/contacts/delta"},
		{"user named like a navigation", "/users/drive/messages", "/v1.0/users/{id}/messages"},
		{"well-known folder only under mailFolders", "/me/messages/inbox", "/v1.0/me/messages/{id}"},
		{"child folder named like a well-known folder", "/me/mailFolders/inbox/childFolders/archive/messages", "/v1.0/me/mailFolders/inbox/childFolders/{id}/messages"},
		{"drive special folder", "/me/drive/special/approot/children", "/v1.0/me/drive/special/approot/children"},
		{"drive item alias", "/me/drive/items/root/children", "/v1.0/me/drive/items/root/children"},
		{"users delta", "/users/delta", "/v1.0/users/delta"},
		{"member ref", "/groups/AAA/members/$ref", "/v1.0/groups/{id}/members/$ref"},
		// Rendering is bounded however long the path.
		// Production paths arrive percent-encoded; each segment is decoded
		// before it is matched.
		{"encoded addressing colons", "/me/drive/root%3A/search/calendar/Secret.txt%3A/content", "/v1.0/me/drive/{id}/content"},
		{"lowercase hex colons", "/me/drive/root%3a/drive/recent%3a", "/v1.0/me/drive/{id}"},
		{"mixed-case hex colons", "/me/Drive/root%3a/Archive%3A/content", "/v1.0/me/drive/{id}/content"},
		{"double-encoded colons", "/me/drive/root%253A/search/calendar/Secret.txt%253A/content", "/v1.0/me/drive/{id}/content"},
		{"encoded slash stays in its segment", "/users/a%2Fmessages/events", "/v1.0/users/{id}/events"},
		{"encoded fixed word", "/me/%6Dessages", "/v1.0/me/messages"},
	}
	client, err := New(staticToken("t"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := client.resolveURL(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if got := routeTemplate(u.EscapedPath()); got != tc.want {
				t.Fatalf("routeTemplate(%q) = %q, want %q", u.EscapedPath(), got, tc.want)
			}
		})
	}
}

// Shapes resolveURL never produces, given to routeTemplate directly.
func TestRouteTemplateDirect(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"empty", "", "/"},
		{"root", "/", "/"},
		{"beta", "/beta/me/messages", "/beta/me/messages"},
		{"trailing and doubled slashes", "//v1.0//me/messages/", "/v1.0/me/messages"},
		{"repeated well-known names", "/inbox/x/inbox/x/inbox/x/inbox/x", "/{id}"},
		{"segment cap", "/v1.0" + strings.Repeat("/me/drive", 20), "/v1.0" + strings.Repeat("/me/drive", 7) + "/me/..."},
		{"undecodable segment", "/v1.0/me/%zz/messages", "/v1.0/me/{id}/messages"},
		{"undecodable segment inside addressing", "/v1.0/me/drive/root:/%zz/a:/content", "/v1.0/me/drive/{id}/content"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := routeTemplate(tc.path); got != tc.want {
				t.Fatalf("routeTemplate(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// The route recorded for a real request is built from the encoded path the
// client sends, so the decoding has to hold end to end.
func TestTraceRedactsEncodedAddressing(t *testing.T) {
	client, err := New(staticToken("t"), WithHTTPClient(&http.Client{Transport: downloadRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	ctx, root := rec.root(t)

	for _, path := range []string{
		"/me/drive/root%3A/search/calendar/Secret.txt%3A/content",
		"/users('someone@example.com')/events",
	} {
		if _, err := client.Get(ctx, path, Params{}, nil); err != nil {
			t.Fatal(err)
		}
	}

	want := "msgraph.request\n  msgraph.token\n  GET /v1.0/me/drive/{id}/content\n" +
		"msgraph.request\n  msgraph.token\n  GET /v1.0/users({id})/events\n"
	if got := rec.tree(root); got != want {
		t.Fatalf("span tree:\n%s\nwant:\n%s", got, want)
	}
	if dump := rec.dump(); strings.Contains(dump, "search") || strings.Contains(dump, "Secret") || strings.Contains(dump, "someone") {
		t.Fatalf("addressed names leaked:\n%s", dump)
	}
}

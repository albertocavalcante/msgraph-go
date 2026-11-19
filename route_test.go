package msgraph

import (
	"strings"
	"testing"
)

func TestRouteTemplate(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"empty", "", "/"},
		{"root", "/", "/"},
		{"me", "/v1.0/me", "/v1.0/me"},
		{"beta", "/beta/me/messages", "/beta/me/messages"},
		{"message id", "/v1.0/me/messages/AAMkADAwATM0MDAAMS1iNTcwLWI2NTEtMDACLTAwCgBGAAA=", "/v1.0/me/messages/{id}"},
		{"folder and message ids", "/v1.0/me/mailFolders/AQMkAGI2/messages/AAMkAGI2TG93", "/v1.0/me/mailFolders/{id}/messages/{id}"},
		{"well-known folder", "/v1.0/me/mailFolders/inbox/messages", "/v1.0/me/mailFolders/inbox/messages"},
		{"well-known folder casing", "/v1.0/me/MailFolders/Inbox/Messages", "/v1.0/me/mailFolders/inbox/messages"},
		{"user principal name", "/v1.0/users/someone@example.com/messages", "/v1.0/users/{id}/messages"},
		{"escaped user principal name", "/v1.0/users/someone%40example.com", "/v1.0/users/{id}"},
		{"user key in parentheses", "/v1.0/users('someone@example.com')/events", "/v1.0/users({id})/events"},
		{"unknown name with parentheses", "/v1.0/reports/getEmailActivityUserDetail(period='D7')", "/v1.0/{id}"},
		{"unbalanced parenthesis", "/v1.0/users(someone", "/v1.0/{id}"},
		{"batch", "/v1.0/$batch", "/v1.0/$batch"},
		{"delta", "/v1.0/me/mailFolders/inbox/messages/delta", "/v1.0/me/mailFolders/inbox/messages/delta"},
		{"delta function", "/v1.0/me/drive/root/delta()", "/v1.0/me/drive/root/delta()"},
		{"delta function with token argument", "/v1.0/me/drive/root/delta(token='abc')", "/v1.0/me/drive/root/delta({id})"},
		{"qualified function", "/v1.0/me/drive/root/microsoft.graph.delta()", "/v1.0/me/drive/root/microsoft.graph.delta()"},
		{"type cast", "/v1.0/me/memberOf/microsoft.graph.group", "/v1.0/me/memberOf/microsoft.graph.group"},
		{"fake qualified name", "/v1.0/me/microsoft.graph.someone@example.com", "/v1.0/me/{id}"},
		{"key equals value", "/v1.0/sites/id=1234", "/v1.0/sites/{id}"},
		{"path-based addressing collapses", "/v1.0/me/drive/root:/Reports/2026/Budget.xlsx:/content", "/v1.0/me/drive/{id}/content"},
		{"site composite id", "/v1.0/sites/contoso.sharepoint.com,1111,2222/lists", "/v1.0/sites/{id}/lists"},
		{"numeric segment", "/v1.0/me/events/12345", "/v1.0/me/events/{id}"},
		{"trailing and doubled slashes", "//v1.0//me/messages/", "/v1.0/me/messages"},
		{"chat message", "/v1.0/chats/19:abc@thread.v2/messages/1700000000000", "/v1.0/chats/{id}/messages/{id}"},
		{"value", "/v1.0/me/messages/AAMk/$value", "/v1.0/me/messages/{id}/$value"},
		{"unknown fixed-looking word", "/v1.0/me/secretProjectName", "/v1.0/me/{id}"},
		// Path-based addressing is one identifier, whatever words it contains.
		{"addressed path with fixed words", "/v1.0/me/drive/root:/Projects/Archive/Contacts/Budget.xlsx:/content", "/v1.0/me/drive/{id}/content"},
		{"addressed path below an item", "/v1.0/me/drive/items/01ABC:/sub/inbox/drafts:/content", "/v1.0/me/drive/items/{id}/content"},
		{"unterminated addressed path", "/v1.0/me/drive/root:/Projects/messages/events", "/v1.0/me/drive/{id}"},
		{"site by path", "/v1.0/sites/contoso.sharepoint.com:/sites/Team:/lists", "/v1.0/sites/{id}/lists"},
		// Qualified names come from an explicit list, not a pattern.
		{"unknown qualified name", "/v1.0/me/microsoft.graph.JohnSmithSecret", "/v1.0/me/{id}"},
		{"very long qualified name", "/v1.0/me/microsoft.graph." + strings.Repeat("a", 5000), "/v1.0/me/{id}"},
		{"cast after a collection", "/v1.0/users/AAA/transitiveMemberOf/microsoft.graph.group", "/v1.0/users/{id}/transitiveMemberOf/microsoft.graph.group"},
		// After a collection the next segment is an identifier unless it is
		// one of that collection's known children.
		{"list title", "/v1.0/sites/root/lists/Tasks/items", "/v1.0/sites/root/lists/{id}/items"},
		{"list title equal to a collection", "/v1.0/sites/SITE/lists/Contacts/items/7", "/v1.0/sites/{id}/lists/{id}/items/{id}"},
		{"todo list", "/v1.0/me/todo/lists/AAA/tasks/BBB", "/v1.0/me/todo/lists/{id}/tasks/{id}"},
		{"todo delta", "/v1.0/me/todo/lists/delta", "/v1.0/me/todo/lists/delta"},
		{"calendar named like a collection", "/v1.0/me/calendars/events/events", "/v1.0/me/calendars/{id}/events"},
		{"contact folder", "/v1.0/me/contactFolders/AAA/contacts/delta", "/v1.0/me/contactFolders/{id}/contacts/delta"},
		{"user named like a navigation", "/v1.0/users/drive/messages", "/v1.0/users/{id}/messages"},
		{"well-known folder only under mailFolders", "/v1.0/me/messages/inbox", "/v1.0/me/messages/{id}"},
		{"child folder named like a well-known folder", "/v1.0/me/mailFolders/inbox/childFolders/archive/messages", "/v1.0/me/mailFolders/inbox/childFolders/{id}/messages"},
		{"repeated well-known names", "/inbox/x/inbox/x/inbox/x/inbox/x", "/{id}"},
		{"drive special folder", "/v1.0/me/drive/special/approot/children", "/v1.0/me/drive/special/approot/children"},
		{"drive item alias", "/v1.0/me/drive/items/root/children", "/v1.0/me/drive/items/root/children"},
		{"users delta", "/v1.0/users/delta", "/v1.0/users/delta"},
		{"member ref", "/v1.0/groups/AAA/members/$ref", "/v1.0/groups/{id}/members/$ref"},
		// Rendering is bounded however long the path.
		{"segment cap", "/v1.0" + strings.Repeat("/me/drive", 20), "/v1.0" + strings.Repeat("/me/drive", 7) + "/me/..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := routeTemplate(tc.path); got != tc.want {
				t.Fatalf("routeTemplate(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

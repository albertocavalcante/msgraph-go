package msgraph

import "testing"

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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := routeTemplate(tc.path); got != tc.want {
				t.Fatalf("routeTemplate(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

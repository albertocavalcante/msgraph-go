package msgraph

import "testing"

func TestFindPermission(t *testing.T) {
	permission, ok := FindPermission("mail.readwrite")
	if !ok {
		t.Fatal("Mail.ReadWrite not found")
	}
	if permission.ID != MailReadWriteDelegatedID {
		t.Fatalf("ID = %q, want %q", permission.ID, MailReadWriteDelegatedID)
	}
	if !permission.PersonalAccountsAllowed {
		t.Fatal("Mail.ReadWrite should be marked valid for personal accounts")
	}

	permission, ok = FindPermission(MailSendDelegatedID)
	if !ok {
		t.Fatal("Mail.Send ID not found")
	}
	if permission.Name != "Mail.Send" {
		t.Fatalf("Name = %q, want Mail.Send", permission.Name)
	}
}

func TestSuggestDelegatedScopes(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		scopes []string
		match  string
	}{
		{name: "me", method: "GET", path: "/me", scopes: []string{ScopeUserRead}, match: "me"},
		{name: "mail read", method: "GET", path: "/me/messages", scopes: []string{ScopeMailRead}, match: "mail"},
		{name: "mail write", method: "PATCH", path: "/me/messages/id", scopes: []string{ScopeMailReadWrite}, match: "mail"},
		{name: "send mail", method: "POST", path: "/me/sendMail", scopes: []string{ScopeMailSend}, match: "sendMail"},
		{name: "calendar read", method: "GET", path: "/me/events", scopes: []string{ScopeCalendarsRead}, match: "calendar"},
		{name: "contact write", method: "DELETE", path: "/me/contacts/id", scopes: []string{ScopeContactsReadWrite}, match: "contacts"},
		{name: "files read", method: "GET", path: "/me/drive/root/children", scopes: []string{ScopeFilesRead}, match: "files"},
		{name: "users read", method: "GET", path: "/users", scopes: []string{ScopeUserReadBasicAll}, match: "users"},
		{name: "groups write", method: "POST", path: "/groups", scopes: []string{ScopeGroupReadWriteAll}, match: "groups"},
		{name: "translate ids", method: "POST", path: "/me/translateExchangeIds", scopes: []string{ScopeUserRead}, match: "translateExchangeIds"},
		{name: "unknown", method: "GET", path: "/auditLogs/signIns", scopes: nil, match: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SuggestDelegatedScopes(tt.method, tt.path)
			if got.Match != tt.match {
				t.Fatalf("Match = %q, want %q", got.Match, tt.match)
			}
			if !equalStrings(got.Scopes, tt.scopes) {
				t.Fatalf("Scopes = %v, want %v", got.Scopes, tt.scopes)
			}
		})
	}
}

func TestSuggestDelegatedScopesAbsoluteURL(t *testing.T) {
	got := SuggestDelegatedScopes("GET", "https://graph.microsoft.com/v1.0/me/messages?$top=1")
	if !equalStrings(got.Scopes, []string{ScopeMailRead}) {
		t.Fatalf("Scopes = %v", got.Scopes)
	}
}

func TestMergeScopes(t *testing.T) {
	got := MergeScopes([]string{ScopeUserRead}, []string{ScopeMailRead, "user.read"})
	want := []string{ScopeUserRead, ScopeMailRead}
	if !equalStrings(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

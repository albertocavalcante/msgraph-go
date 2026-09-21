package msgraph

import (
	"net/http"
	"testing"
)

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

// Mailbox settings need their own permission, and the service does not say so
// when it is missing: it answers a bare ErrorAccessDenied. The suggestion is
// the only place a caller can learn what to ask for.
func TestSuggestsMailboxSettingsScopes(t *testing.T) {
	read := SuggestDelegatedScopes("GET", "/me/mailboxSettings")
	if len(read.Scopes) != 1 || read.Scopes[0] != ScopeMailboxSettingsRead {
		t.Fatalf("read scopes = %v, want %s", read.Scopes, ScopeMailboxSettingsRead)
	}
	if len(read.Notes) == 0 {
		t.Fatal("no note explaining why Mail.ReadWrite is not enough")
	}

	write := SuggestDelegatedScopes("PATCH", "/me/mailboxSettings")
	if len(write.Scopes) != 1 || write.Scopes[0] != ScopeMailboxSettingsReadWrite {
		t.Fatalf("write scopes = %v, want %s", write.Scopes, ScopeMailboxSettingsReadWrite)
	}

	// Mail scopes must not be suggested for it, which was the mistake that
	// made this a production-only discovery.
	for _, scope := range append(read.Scopes, write.Scopes...) {
		if scope == ScopeMailRead || scope == ScopeMailReadWrite {
			t.Fatalf("suggested a mail scope %q for mailbox settings", scope)
		}
	}
}

// TestTeamsPathsDoNotResolveToMailScopes pins the routing rule that a Teams
// message path is not a mail path.
//
// Both spell a collection "messages", and the mail case matches any path with
// that segment, so /chats/{id}/messages and a channel's messages resolved to
// Mail.Read. Nothing caught it because the suggestion is only consulted by
// RequireScopes, which no Teams test existed to run -- so the wrong answer was
// never returned to anyone who would notice. A caller holding every Teams
// permission and no mail permission would have been refused, citing a mail
// scope that has nothing to do with the request.
//
// The expected scopes are spelled as literals rather than referenced through
// the new constants, on purpose. The constants are part of what is under test:
// written against them, this would keep passing through a typo in one,
// agreeing with the code about a permission name the identity service has
// never heard of.
func TestTeamsPathsDoNotResolveToMailScopes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		method string
		path   string
		want   string
		match  string
	}{
		{"chat list", http.MethodGet, "/chats", "Chat.ReadBasic", "chat"},
		{"chat messages", http.MethodGet, "/chats/19:abc@thread.v2/messages", "Chat.Read", "chatMessage"},
		{"chat message send", http.MethodPost, "/chats/19:abc@thread.v2/messages", "ChatMessage.Send", "chatMessage"},
		{"joined teams", http.MethodGet, "/me/joinedTeams", "Team.ReadBasic.All", "team"},
		{"channels", http.MethodGet, "/teams/id/channels", "Channel.ReadBasic.All", "channel"},
		{"channel messages", http.MethodGet, "/teams/id/channels/19:x@thread.tacv2/messages", "ChannelMessage.Read.All", "channelMessage"},
		{"channel replies", http.MethodGet, "/teams/id/channels/19:x@thread.tacv2/messages/1/replies", "ChannelMessage.Read.All", "channelMessage"},
		{"presence", http.MethodGet, "/me/presence", "Presence.Read", "presence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			suggestion := SuggestDelegatedScopes(tc.method, tc.path)

			// The negative control, and the whole reason this test exists: a
			// mail scope for a Teams path is not merely imprecise, it names a
			// permission that cannot grant the request.
			for _, scope := range suggestion.Scopes {
				if scope == ScopeMailRead || scope == ScopeMailReadWrite || scope == ScopeMailSend {
					t.Fatalf("%s %s suggested mail scope %q", tc.method, tc.path, scope)
				}
			}
			if len(suggestion.Scopes) == 0 || suggestion.Scopes[0] != tc.want {
				t.Fatalf("%s %s scopes = %v, want %s first", tc.method, tc.path, suggestion.Scopes, tc.want)
			}
			if suggestion.Match != tc.match {
				t.Fatalf("%s %s match = %q, want %q", tc.method, tc.path, suggestion.Match, tc.match)
			}
		})
	}
}

// TestChannelScopesAreMarkedAdminOnly pins which Teams permissions a signed-in
// user can consent to for themselves.
//
// It matters because the remedy differs. A missing Chat.Read is fixed by
// signing in again and asking for it; a missing ChannelMessage.Read.All is
// not, and telling someone to grant it is advice they cannot act on. A caller
// that treats the two alike loops against a consent screen that will never
// offer the second.
func TestChannelScopesAreMarkedAdminOnly(t *testing.T) {
	t.Parallel()

	for name, wantAdmin := range map[string]bool{
		"Chat.ReadBasic":          false,
		"Chat.Read":               false,
		"Chat.ReadWrite":          false,
		"ChatMessage.Send":        false,
		"Presence.Read":           false,
		"Team.ReadBasic.All":      true,
		"Channel.ReadBasic.All":   true,
		"ChannelMessage.Read.All": true,
		"ChannelMessage.Send":     false,
	} {
		permission, ok := FindPermission(name)
		if !ok {
			t.Errorf("%s is not a known permission", name)
			continue
		}
		if permission.AdminConsentRequired != wantAdmin {
			t.Errorf("%s AdminConsentRequired = %v, want %v", name, permission.AdminConsentRequired, wantAdmin)
		}
		// None of the Teams permissions work on a personal Microsoft account;
		// every one of these endpoints is documented as work-or-school only.
		if permission.PersonalAccountsAllowed {
			t.Errorf("%s must not be marked valid for personal accounts", name)
		}
	}
}

// TestChatScopesNest pins that the broader chat permission stands in for the
// narrower one, which is what lets a caller holding Chat.ReadWrite skip a
// second consent for Chat.Read.
func TestChatScopesNest(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		granted  string
		required string
		want     bool
	}{
		{"Chat.ReadWrite", "Chat.Read", true},
		{"Chat.ReadWrite", "Chat.ReadBasic", true},
		{"Chat.Read", "Chat.ReadBasic", true},
		{"Chat.ReadWrite", "ChatMessage.Send", true},
		// Not the other way around: reading a chat does not let you post to it.
		{"Chat.Read", "Chat.ReadWrite", false},
		{"Chat.ReadBasic", "Chat.Read", false},
		{"ChatMessage.Send", "Chat.Read", false},
		// And a chat permission never reaches a channel.
		{"Chat.ReadWrite", "ChannelMessage.Read.All", false},
	} {
		if got := ScopeSatisfies([]string{tc.granted}, tc.required); got != tc.want {
			t.Errorf("ScopeSatisfies([%s], %s) = %v, want %v", tc.granted, tc.required, got, tc.want)
		}
	}
}

// TestInferenceClassificationUsesMailScopes pins a routing rule that reads
// backwards. Focused Inbox overrides look like a mailbox setting and are
// governed by the mail permissions instead, so a caller holding
// MailboxSettings.ReadWrite alone is refused.
func TestInferenceClassificationUsesMailScopes(t *testing.T) {
	t.Parallel()

	const path = "/me/inferenceClassification/overrides"
	for _, tc := range []struct {
		method string
		want   string
	}{
		{http.MethodGet, ScopeMailRead},
		{http.MethodPost, ScopeMailReadWrite},
		{http.MethodPatch, ScopeMailReadWrite},
		{http.MethodDelete, ScopeMailReadWrite},
	} {
		suggestion := SuggestDelegatedScopes(tc.method, path)
		if len(suggestion.Scopes) != 1 || suggestion.Scopes[0] != tc.want {
			t.Errorf("%s %s = %v, want [%s]", tc.method, path, suggestion.Scopes, tc.want)
		}
		if ScopeSatisfies([]string{ScopeMailboxSettingsReadWrite}, suggestion.Scopes[0]) {
			t.Errorf("%s: MailboxSettings.ReadWrite must not satisfy %s", tc.method, suggestion.Scopes[0])
		}
	}
}

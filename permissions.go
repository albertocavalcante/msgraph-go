package msgraph

import (
	"net/http"
	"net/url"
	"strings"
)

const (
	// GraphResourceAppID is the Microsoft Graph service principal app ID.
	GraphResourceAppID = "00000003-0000-0000-c000-000000000000"

	// GraphCLIClientID is Microsoft's first-party Microsoft Graph Command Line
	// Tools public client app ID.
	GraphCLIClientID = "14d82eec-204b-4c2f-b7e8-296a70dab67e"

	MailReadDelegatedID      = "570282fd-fa5c-430d-a7fd-fc8dc98a9dca"
	MailReadWriteDelegatedID = "024d486e-b451-40bb-833d-3e66d98c5c73"
	MailSendDelegatedID      = "e383f46e-2787-4529-855e-0e479a3ffac0"
	UserReadDelegatedID      = "e1fe6dd8-ba31-4d61-89e7-88639da4683d"
)

const (
	ScopeUserRead         = "User.Read"
	ScopeUserReadBasicAll = "User.ReadBasic.All"
	ScopeUserReadWriteAll = "User.ReadWrite.All"
	ScopeMailRead         = "Mail.Read"
	ScopeMailReadWrite    = "Mail.ReadWrite"
	ScopeMailSend         = "Mail.Send"
	// ScopeMailboxSettingsRead is required to read mailbox settings, including
	// automatic replies. Mail.ReadWrite does not cover it, and the service
	// answers a request without it with a bare ErrorAccessDenied that names no
	// permission.
	ScopeMailboxSettingsRead = "MailboxSettings.Read"
	// ScopeMailboxSettingsReadWrite is required to change them.
	ScopeMailboxSettingsReadWrite = "MailboxSettings.ReadWrite"
	ScopeCalendarsRead            = "Calendars.Read"
	ScopeCalendarsReadWrite       = "Calendars.ReadWrite"
	ScopeTasksRead                = "Tasks.Read"
	ScopeTasksReadWrite           = "Tasks.ReadWrite"
	ScopeContactsRead             = "Contacts.Read"
	ScopeContactsReadWrite        = "Contacts.ReadWrite"
	ScopeFilesRead                = "Files.Read"
	ScopeFilesReadWrite           = "Files.ReadWrite"
	ScopeGroupReadAll             = "Group.Read.All"
	ScopeGroupReadWriteAll        = "Group.ReadWrite.All"
	ScopeDirectoryReadAll         = "Directory.Read.All"
)

// Microsoft Teams delegated permissions.
//
// The chat scopes and the channel scopes are not two halves of one thing. A
// signed-in user can consent to every chat scope themselves; not one of the
// channel scopes can be granted without a tenant administrator, because they
// are tenant-wide by construction -- there is no delegated permission that
// reads only the channels you are in. That split is why AdminConsentRequired
// is set the way it is below, and it is the difference between a remedy a
// caller can act on and one they cannot.
const (
	// ScopeChatReadBasic lists chats and their members without reading what
	// was said in them.
	ScopeChatReadBasic = "Chat.ReadBasic"
	// ScopeChatRead reads chat messages.
	ScopeChatRead = "Chat.Read"
	// ScopeChatReadWrite reads chat messages and changes them: reactions and
	// soft deletes need it, and ChatMessage.Send does not cover either.
	ScopeChatReadWrite = "Chat.ReadWrite"
	// ScopeChatMessageSend sends a chat message and nothing else. It does not
	// permit reading the chat it posts to.
	ScopeChatMessageSend = "ChatMessage.Send"
	// ScopeTeamReadBasicAll lists the teams the signed-in user belongs to.
	ScopeTeamReadBasicAll = "Team.ReadBasic.All"
	// ScopeChannelReadBasicAll lists a team's channels. Team.ReadBasic.All
	// does not cover it: listing teams and listing their channels are
	// separate permissions.
	ScopeChannelReadBasicAll = "Channel.ReadBasic.All"
	// ScopeChannelMessageReadAll reads channel messages and their replies.
	ScopeChannelMessageReadAll = "ChannelMessage.Read.All"
	// ScopeChannelMessageSend posts to a channel. Unlike its read
	// counterpart it is user-consentable, so an app can be allowed to post
	// where it is not allowed to read.
	ScopeChannelMessageSend = "ChannelMessage.Send"
	// ScopePresenceRead reads the signed-in user's own presence.
	ScopePresenceRead = "Presence.Read"
	// ScopePresenceReadAll reads anyone's presence, and is a different
	// permission rather than a wider setting of the same one.
	ScopePresenceReadAll = "Presence.Read.All"
)

// Permission describes a known Microsoft Graph permission.
type Permission struct {
	Name                    string
	ID                      string
	Kind                    string
	AdminConsentRequired    bool
	PersonalAccountsAllowed bool
}

// PermissionSuggestion describes delegated scopes likely needed for a raw Graph
// request. It is a conservative route-pattern hint, not a substitute for the
// endpoint-specific Microsoft documentation.
type PermissionSuggestion struct {
	Method string
	Path   string
	Match  string
	Scopes []string
	Notes  []string
}

// CommonPermissions is a hand-curated seed set. The full catalog should be
// generated from the Graph service principal in a later package.
var CommonPermissions = []Permission{
	{
		Name:                    ScopeMailRead,
		ID:                      MailReadDelegatedID,
		Kind:                    "delegated",
		PersonalAccountsAllowed: true,
	},
	{
		Name:                    ScopeMailReadWrite,
		ID:                      MailReadWriteDelegatedID,
		Kind:                    "delegated",
		PersonalAccountsAllowed: true,
	},
	{
		Name:                    ScopeMailSend,
		ID:                      MailSendDelegatedID,
		Kind:                    "delegated",
		PersonalAccountsAllowed: true,
	},
	{
		Name:                    ScopeUserRead,
		ID:                      UserReadDelegatedID,
		Kind:                    "delegated",
		PersonalAccountsAllowed: true,
	},
	{Name: ScopeUserReadBasicAll, Kind: "delegated"},
	{Name: ScopeUserReadWriteAll, Kind: "delegated", AdminConsentRequired: true},
	{Name: ScopeMailboxSettingsRead, Kind: "delegated", PersonalAccountsAllowed: true},
	{Name: ScopeMailboxSettingsReadWrite, Kind: "delegated", PersonalAccountsAllowed: true},
	{Name: ScopeCalendarsRead, Kind: "delegated", PersonalAccountsAllowed: true},
	{Name: ScopeCalendarsReadWrite, Kind: "delegated", PersonalAccountsAllowed: true},
	{Name: ScopeTasksRead, Kind: "delegated", PersonalAccountsAllowed: true},
	{Name: ScopeTasksReadWrite, Kind: "delegated", PersonalAccountsAllowed: true},
	{Name: ScopeContactsRead, Kind: "delegated", PersonalAccountsAllowed: true},
	{Name: ScopeContactsReadWrite, Kind: "delegated", PersonalAccountsAllowed: true},
	{Name: ScopeFilesRead, Kind: "delegated", PersonalAccountsAllowed: true},
	{Name: ScopeFilesReadWrite, Kind: "delegated", PersonalAccountsAllowed: true},
	{Name: ScopeGroupReadAll, Kind: "delegated", AdminConsentRequired: true},
	{Name: ScopeGroupReadWriteAll, Kind: "delegated", AdminConsentRequired: true},
	{Name: ScopeDirectoryReadAll, Kind: "delegated", AdminConsentRequired: true},

	// Teams. None are valid for personal Microsoft accounts: every Teams
	// endpoint is documented work-or-school only, and a consumer account is
	// refused at the identity service rather than at the API.
	{Name: ScopeChatReadBasic, Kind: "delegated"},
	{Name: ScopeChatRead, Kind: "delegated"},
	{Name: ScopeChatReadWrite, Kind: "delegated"},
	{Name: ScopeChatMessageSend, Kind: "delegated"},
	{Name: ScopeTeamReadBasicAll, Kind: "delegated", AdminConsentRequired: true},
	{Name: ScopeChannelReadBasicAll, Kind: "delegated", AdminConsentRequired: true},
	{Name: ScopeChannelMessageReadAll, Kind: "delegated", AdminConsentRequired: true},
	{Name: ScopeChannelMessageSend, Kind: "delegated"},
	{Name: ScopePresenceRead, Kind: "delegated"},
	{Name: ScopePresenceReadAll, Kind: "delegated", AdminConsentRequired: true},
}

// FindPermission returns a known permission by name or ID.
func FindPermission(nameOrID string) (Permission, bool) {
	for _, permission := range CommonPermissions {
		if strings.EqualFold(permission.Name, nameOrID) || strings.EqualFold(permission.ID, nameOrID) {
			return permission, true
		}
	}
	return Permission{}, false
}

// SuggestDelegatedScopes returns a least-privilege delegated-scope hint for
// common Microsoft Graph routes. Unknown routes return an empty scope list.
func SuggestDelegatedScopes(method, rawPath string) PermissionSuggestion {
	method = strings.ToUpper(strings.TrimSpace(method))
	path := normalizeSuggestionPath(rawPath)
	read := isSuggestionReadMethod(method)
	suggestion := PermissionSuggestion{Method: method, Path: path}

	switch {
	case path == "/me":
		suggestion.Match = "me"
		suggestion.Scopes = []string{ScopeUserRead}
	case strings.Contains(path, "/translateexchangeids"):
		suggestion.Match = "translateExchangeIds"
		suggestion.Scopes = []string{ScopeUserRead}
		suggestion.Notes = append(suggestion.Notes, "For work or school accounts, Microsoft lists User.ReadBasic.All as least privileged; User.Read keeps personal Microsoft accounts covered.")
	case strings.Contains(path, "/mailboxsettings"):
		suggestion.Match = "mailboxSettings"
		if read {
			suggestion.Scopes = []string{ScopeMailboxSettingsRead}
		} else {
			suggestion.Scopes = []string{ScopeMailboxSettingsReadWrite}
		}
		suggestion.Notes = append(suggestion.Notes,
			"Mail.ReadWrite does not cover mailbox settings; without MailboxSettings.* the service answers ErrorAccessDenied without naming the permission.")
	case strings.HasPrefix(path, "/me/todo"):
		// To Do is a separate service reached through Graph. It is addressed
		// under /me/todo only: there is no delegated form for another user's
		// lists, so this route never carries a /users/{id} prefix.
		suggestion.Match = "todo"
		if read {
			suggestion.Scopes = []string{ScopeTasksRead}
		} else {
			suggestion.Scopes = []string{ScopeTasksReadWrite}
		}
	case strings.Contains(path, "/inferenceclassification"):
		// Focused Inbox overrides read as a mailbox setting and are not one.
		// They sit under the mail permissions, so a caller holding
		// MailboxSettings.ReadWrite and no mail scope is refused -- the exact
		// opposite of the mailboxSettings case above.
		suggestion.Match = "inferenceClassification"
		if read {
			suggestion.Scopes = []string{ScopeMailRead}
		} else {
			suggestion.Scopes = []string{ScopeMailReadWrite}
		}
		suggestion.Notes = append(suggestion.Notes,
			"Focused Inbox overrides use Mail.* despite looking like a mailbox setting; MailboxSettings.* does not cover them.")
	case strings.Contains(path, "/sendmail"):
		suggestion.Match = "sendMail"
		suggestion.Scopes = []string{ScopeMailSend}
	// The Teams cases must precede the mail case below. Both services call a
	// collection "messages", the mail case matches that segment anywhere in
	// the path, and so /chats/{id}/messages resolved to Mail.Read -- naming a
	// permission that cannot grant the request, to a caller who very likely
	// holds no mail permission at all.
	case strings.Contains(path, "/search/query"):
		suggestion.Match = "search"
		suggestion.Scopes = []string{ScopeChatRead}
		suggestion.Notes = append(suggestion.Notes,
			"Search reaches only what the caller was already permitted to read: with Chat.Read it returns chat hits and no channel hits, without saying so. Channel results additionally need ChannelMessage.Read.All.")
	case pathContainsAnySegment(path, "presence", "presences"):
		suggestion.Match = "presence"
		if strings.HasPrefix(path, "/me/") {
			suggestion.Scopes = []string{ScopePresenceRead}
		} else {
			suggestion.Scopes = []string{ScopePresenceReadAll}
			suggestion.Notes = append(suggestion.Notes,
				"Reading another user's presence needs Presence.Read.All; Presence.Read covers only your own.")
		}
	case pathContainsAnySegment(path, "chats"):
		if pathContainsAnySegment(path, "messages") {
			suggestion.Match = "chatMessage"
			switch {
			case read:
				suggestion.Scopes = []string{ScopeChatRead}
			case pathContainsAnySegment(path, "setreaction", "unsetreaction", "softdelete", "undosoftdelete"):
				// Reacting and deleting are not sending. ChatMessage.Send
				// covers posting a message and nothing that happens to one
				// afterwards, so these need the broader chat permission.
				suggestion.Scopes = []string{ScopeChatReadWrite}
			default:
				suggestion.Scopes = []string{ScopeChatMessageSend}
			}
		} else {
			suggestion.Match = "chat"
			if read {
				suggestion.Scopes = []string{ScopeChatReadBasic}
			} else {
				suggestion.Scopes = []string{ScopeChatReadWrite}
			}
		}
	case pathContainsAnySegment(path, "teams", "joinedteams", "channels"):
		switch {
		case pathContainsAnySegment(path, "messages", "replies"):
			suggestion.Match = "channelMessage"
			if read {
				suggestion.Scopes = []string{ScopeChannelMessageReadAll}
			} else {
				suggestion.Scopes = []string{ScopeChannelMessageSend}
			}
		case pathContainsAnySegment(path, "channels"):
			suggestion.Match = "channel"
			suggestion.Scopes = []string{ScopeChannelReadBasicAll}
		default:
			suggestion.Match = "team"
			suggestion.Scopes = []string{ScopeTeamReadBasicAll}
		}
		if len(suggestion.Scopes) > 0 && suggestion.Scopes[0] != ScopeChannelMessageSend {
			suggestion.Notes = append(suggestion.Notes,
				"Team and channel permissions are tenant-wide and require administrator consent; a signed-in user cannot grant them.")
		}
	case pathContainsAnySegment(path, "messages", "mailfolders"):
		suggestion.Match = "mail"
		if read {
			suggestion.Scopes = []string{ScopeMailRead}
		} else {
			suggestion.Scopes = []string{ScopeMailReadWrite}
		}
	case pathContainsAnySegment(path, "events", "calendar", "calendars"):
		suggestion.Match = "calendar"
		if read {
			suggestion.Scopes = []string{ScopeCalendarsRead}
		} else {
			suggestion.Scopes = []string{ScopeCalendarsReadWrite}
		}
	case pathContainsAnySegment(path, "contacts", "contactfolders"):
		suggestion.Match = "contacts"
		if read {
			suggestion.Scopes = []string{ScopeContactsRead}
		} else {
			suggestion.Scopes = []string{ScopeContactsReadWrite}
		}
	case pathContainsAnySegment(path, "drive", "drives", "driveitems"):
		suggestion.Match = "files"
		if read {
			suggestion.Scopes = []string{ScopeFilesRead}
		} else {
			suggestion.Scopes = []string{ScopeFilesReadWrite}
		}
	case pathContainsAnySegment(path, "users"):
		suggestion.Match = "users"
		if read {
			suggestion.Scopes = []string{ScopeUserReadBasicAll}
		} else {
			suggestion.Scopes = []string{ScopeUserReadWriteAll}
			suggestion.Notes = append(suggestion.Notes, "User write operations commonly require tenant admin consent.")
		}
	case pathContainsAnySegment(path, "groups"):
		suggestion.Match = "groups"
		if read {
			suggestion.Scopes = []string{ScopeGroupReadAll}
		} else {
			suggestion.Scopes = []string{ScopeGroupReadWriteAll}
		}
		suggestion.Notes = append(suggestion.Notes, "Group.* permissions commonly require tenant admin consent and are not for personal Microsoft accounts.")
	case pathContainsAnySegment(path, "directoryroles", "directoryroletemplates", "organization"):
		suggestion.Match = "directory"
		suggestion.Scopes = []string{ScopeDirectoryReadAll}
		suggestion.Notes = append(suggestion.Notes, "Directory permissions commonly require tenant admin consent and work or school accounts.")
	}
	suggestion.Scopes = MergeScopes(suggestion.Scopes)
	return suggestion
}

// impliesScope lists the permissions that satisfy a required one. A broader
// permission stands in for a narrower one, which is why holding Mail.ReadWrite
// is enough where Mail.Read is asked for.
var impliesScope = map[string][]string{
	ScopeUserRead:                 {ScopeUserRead, ScopeUserReadBasicAll, ScopeUserReadWriteAll},
	ScopeUserReadBasicAll:         {ScopeUserReadBasicAll, ScopeUserReadWriteAll},
	ScopeUserReadWriteAll:         {ScopeUserReadWriteAll},
	ScopeMailRead:                 {ScopeMailRead, ScopeMailReadWrite},
	ScopeMailReadWrite:            {ScopeMailReadWrite},
	ScopeMailSend:                 {ScopeMailSend, ScopeMailReadWrite},
	ScopeMailboxSettingsRead:      {ScopeMailboxSettingsRead, ScopeMailboxSettingsReadWrite},
	ScopeMailboxSettingsReadWrite: {ScopeMailboxSettingsReadWrite},
	ScopeCalendarsRead:            {ScopeCalendarsRead, ScopeCalendarsReadWrite},
	ScopeCalendarsReadWrite:       {ScopeCalendarsReadWrite},
	ScopeTasksRead:                {ScopeTasksRead, ScopeTasksReadWrite},
	ScopeTasksReadWrite:           {ScopeTasksReadWrite},
	ScopeContactsRead:             {ScopeContactsRead, ScopeContactsReadWrite},
	ScopeContactsReadWrite:        {ScopeContactsReadWrite},
	ScopeFilesRead:                {ScopeFilesRead, ScopeFilesReadWrite},
	ScopeFilesReadWrite:           {ScopeFilesReadWrite},

	// The chat scopes nest; the channel scopes do not nest into them. Holding
	// every chat permission grants nothing in a channel, which is the one
	// substitution a caller is most likely to assume works.
	ScopeChatReadBasic:   {ScopeChatReadBasic, ScopeChatRead, ScopeChatReadWrite},
	ScopeChatRead:        {ScopeChatRead, ScopeChatReadWrite},
	ScopeChatReadWrite:   {ScopeChatReadWrite},
	ScopeChatMessageSend: {ScopeChatMessageSend, ScopeChatReadWrite},

	ScopeTeamReadBasicAll:      {ScopeTeamReadBasicAll, ScopeDirectoryReadAll},
	ScopeChannelReadBasicAll:   {ScopeChannelReadBasicAll, ScopeGroupReadAll, ScopeGroupReadWriteAll, ScopeDirectoryReadAll},
	ScopeChannelMessageReadAll: {ScopeChannelMessageReadAll, ScopeGroupReadAll, ScopeGroupReadWriteAll},
	ScopeChannelMessageSend:    {ScopeChannelMessageSend, ScopeGroupReadWriteAll},

	ScopePresenceRead:    {ScopePresenceRead, ScopePresenceReadAll},
	ScopePresenceReadAll: {ScopePresenceReadAll},
}

// ScopeSatisfies reports whether the granted permissions cover the required
// one, accounting for a broader permission standing in for a narrower one.
//
// Comparison is case-insensitive: the service echoes granted scopes back with
// inconsistent casing, and treating a case difference as a missing permission
// is a confusing way to fail.
func ScopeSatisfies(granted []string, required string) bool {
	accepted, known := impliesScope[required]
	if !known {
		accepted = []string{required}
	}
	for _, candidate := range accepted {
		for _, held := range granted {
			if strings.EqualFold(strings.TrimSpace(held), candidate) {
				return true
			}
		}
	}
	return false
}

// MergeScopes merges scope slices, preserving first-seen spelling and order.
func MergeScopes(values ...[]string) []string {
	var scopes []string
	seen := map[string]bool{}
	for _, group := range values {
		for _, scope := range group {
			scope = strings.TrimSpace(scope)
			if scope == "" {
				continue
			}
			key := strings.ToLower(scope)
			if seen[key] {
				continue
			}
			seen[key] = true
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

func normalizeSuggestionPath(rawPath string) string {
	rawPath = strings.TrimSpace(rawPath)
	if parsed, err := url.Parse(rawPath); err == nil && parsed.Path != "" {
		rawPath = parsed.Path
	}
	rawPath = "/" + strings.TrimLeft(rawPath, "/")
	rawPath = strings.ToLower(rawPath)
	for _, prefix := range []string{"/v1.0/", "/beta/"} {
		if strings.HasPrefix(rawPath, prefix) {
			return "/" + strings.TrimPrefix(rawPath, prefix)
		}
	}
	return rawPath
}

func isSuggestionReadMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func pathContainsAnySegment(path string, names ...string) bool {
	parts := strings.Split(path, "/")
	for _, part := range parts {
		for _, name := range names {
			if part == name {
				return true
			}
		}
	}
	return false
}

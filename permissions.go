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
	ScopeUserRead           = "User.Read"
	ScopeUserReadBasicAll   = "User.ReadBasic.All"
	ScopeUserReadWriteAll   = "User.ReadWrite.All"
	ScopeMailRead           = "Mail.Read"
	ScopeMailReadWrite      = "Mail.ReadWrite"
	ScopeMailSend           = "Mail.Send"
	ScopeCalendarsRead      = "Calendars.Read"
	ScopeCalendarsReadWrite = "Calendars.ReadWrite"
	ScopeContactsRead       = "Contacts.Read"
	ScopeContactsReadWrite  = "Contacts.ReadWrite"
	ScopeFilesRead          = "Files.Read"
	ScopeFilesReadWrite     = "Files.ReadWrite"
	ScopeGroupReadAll       = "Group.Read.All"
	ScopeGroupReadWriteAll  = "Group.ReadWrite.All"
	ScopeDirectoryReadAll   = "Directory.Read.All"
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
	{Name: ScopeCalendarsRead, Kind: "delegated", PersonalAccountsAllowed: true},
	{Name: ScopeCalendarsReadWrite, Kind: "delegated", PersonalAccountsAllowed: true},
	{Name: ScopeContactsRead, Kind: "delegated", PersonalAccountsAllowed: true},
	{Name: ScopeContactsReadWrite, Kind: "delegated", PersonalAccountsAllowed: true},
	{Name: ScopeFilesRead, Kind: "delegated", PersonalAccountsAllowed: true},
	{Name: ScopeFilesReadWrite, Kind: "delegated", PersonalAccountsAllowed: true},
	{Name: ScopeGroupReadAll, Kind: "delegated", AdminConsentRequired: true},
	{Name: ScopeGroupReadWriteAll, Kind: "delegated", AdminConsentRequired: true},
	{Name: ScopeDirectoryReadAll, Kind: "delegated", AdminConsentRequired: true},
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
	case strings.Contains(path, "/sendmail"):
		suggestion.Match = "sendMail"
		suggestion.Scopes = []string{ScopeMailSend}
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

package msgraph

import "strings"

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

// Permission describes a known Microsoft Graph permission.
type Permission struct {
	Name                    string
	ID                      string
	Kind                    string
	AdminConsentRequired    bool
	PersonalAccountsAllowed bool
}

// CommonPermissions is a hand-curated seed set. The full catalog should be
// generated from the Graph service principal in a later package.
var CommonPermissions = []Permission{
	{
		Name:                    "Mail.Read",
		ID:                      MailReadDelegatedID,
		Kind:                    "delegated",
		PersonalAccountsAllowed: true,
	},
	{
		Name:                    "Mail.ReadWrite",
		ID:                      MailReadWriteDelegatedID,
		Kind:                    "delegated",
		PersonalAccountsAllowed: true,
	},
	{
		Name:                    "Mail.Send",
		ID:                      MailSendDelegatedID,
		Kind:                    "delegated",
		PersonalAccountsAllowed: true,
	},
	{
		Name:                    "User.Read",
		ID:                      UserReadDelegatedID,
		Kind:                    "delegated",
		PersonalAccountsAllowed: true,
	},
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

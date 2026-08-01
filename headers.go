package msgraph

import "fmt"

const (
	// PreferIDTypeImmutableID asks Graph to return IDs that survive moves.
	PreferIDTypeImmutableID = `IdType="ImmutableId"`

	// PreferBodyContentTypeText asks Outlook mail endpoints to return text bodies.
	PreferBodyContentTypeText = `outlook.body-content-type="text"`

	// PreferBodyContentTypeHTML asks Outlook mail endpoints to return HTML bodies.
	PreferBodyContentTypeHTML = `outlook.body-content-type="html"`

	// ConsistencyLevelEventual enables Graph advanced query capabilities where
	// endpoints require the ConsistencyLevel header.
	ConsistencyLevelEventual = "eventual"
)

// BodyContentTypePreference returns an Outlook body-content Prefer directive.
func BodyContentTypePreference(contentType string) string {
	return fmt.Sprintf(`outlook.body-content-type=%q`, contentType)
}

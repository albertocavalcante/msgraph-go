package msgraph

import (
	"fmt"
	"strings"
)

// PreferDirective is a single value for the Prefer request header. The named
// constants cover the directives this client is most often used with; any
// other directive can be spelled out with a conversion.
type PreferDirective string

const (
	// PreferIDTypeImmutableID asks Graph to return IDs that survive moves.
	PreferIDTypeImmutableID PreferDirective = `IdType="ImmutableId"`

	// PreferBodyContentTypeText asks Outlook mail endpoints to return text bodies.
	PreferBodyContentTypeText PreferDirective = `outlook.body-content-type="text"`

	// PreferBodyContentTypeHTML asks Outlook mail endpoints to return HTML bodies.
	PreferBodyContentTypeHTML PreferDirective = `outlook.body-content-type="html"`

	// PreferReturnMinimal asks Graph to omit the resource from a write response.
	PreferReturnMinimal PreferDirective = "return=minimal"

	// PreferReturnRepresentation asks Graph to echo the written resource back.
	PreferReturnRepresentation PreferDirective = "return=representation"
)

// ConsistencyLevel is a value for the ConsistencyLevel request header.
type ConsistencyLevel string

// ConsistencyLevelEventual enables Graph advanced query capabilities where
// endpoints require the ConsistencyLevel header. It generally has to be paired
// with [Params.Count].
const ConsistencyLevelEventual ConsistencyLevel = "eventual"

// BodyContentTypePreference returns an Outlook body-content Prefer directive.
func BodyContentTypePreference(contentType string) PreferDirective {
	return PreferDirective(fmt.Sprintf(`outlook.body-content-type=%q`, contentType))
}

// TimeZonePreference returns a Prefer directive asking Outlook to render date
// and time properties in the named Windows or IANA time zone.
func TimeZonePreference(timeZone string) PreferDirective {
	return PreferDirective(fmt.Sprintf("outlook.timezone=%q", timeZone))
}

// MaxPageSizePreference returns a Prefer directive capping server page size.
// Graph uses it on endpoints that ignore $top, such as mail delta queries.
func MaxPageSizePreference(size int) PreferDirective {
	return PreferDirective(fmt.Sprintf("odata.maxpagesize=%d", size))
}

func joinPreferDirectives(directives []PreferDirective) string {
	parts := make([]string, 0, len(directives))
	for _, directive := range directives {
		if value := strings.TrimSpace(string(directive)); value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, ", ")
}

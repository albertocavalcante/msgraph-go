package msgraph

import (
	"net/url"
	"testing"
)

func TestParamsValues(t *testing.T) {
	params := Params{
		Select:  []string{"id", "subject"},
		Expand:  []string{"attachments"},
		OrderBy: []string{"receivedDateTime desc"},
		Search:  `"quarterly report"`,
		Filter:  "isRead eq false",
		Top:     25,
		Skip:    50,
		Custom:  url.Values{"ConsistencyLevel": []string{"eventual"}},
	}
	values := params.Values()
	checks := map[string]string{
		"$select":          "id,subject",
		"$expand":          "attachments",
		"$orderby":         "receivedDateTime desc",
		"$search":          `"quarterly report"`,
		"$filter":          "isRead eq false",
		"$top":             "25",
		"$skip":            "50",
		"ConsistencyLevel": "eventual",
	}
	for key, want := range checks {
		if got := values.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

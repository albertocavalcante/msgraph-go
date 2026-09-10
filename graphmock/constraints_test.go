package graphmock

import "testing"

// The mock parses the filter off the wire, where the typed tree is long gone,
// so its scanner needs its own tests. A lambda's bound variable must not be
// mistaken for a property, and a literal must never be read as one.
func TestFilterFields(t *testing.T) {
	tests := []struct {
		name   string
		filter string
		want   []string
	}{
		{"empty", "", nil},
		{"comparison", "isRead eq false", []string{"isRead"}},
		{"path", "from/emailAddress/address eq 'a@b.com'", []string{"from/emailAddress/address"}},
		{"function", "contains(subject,'invoice')", []string{"subject"}},
		{
			"conjunction",
			"(isRead eq false and receivedDateTime ge 2026-08-17T12:30:00Z)",
			[]string{"isRead", "receivedDateTime"},
		},
		// The bound variable is not a property of the resource.
		{"lambda", "categories/any(c:c eq 'Red')", []string{"categories"}},
		{"lambda with spaces in the literal", "categories/any(c:c eq 'Red Team')", []string{"categories"}},
		{
			"lambda beside a comparison",
			"(isRead eq false and categories/any(c:c eq 'Red'))",
			[]string{"isRead", "categories"},
		},
		{
			"recipient lambda reports the collection",
			"toRecipients/any(r:r/emailAddress/address eq 'a@b.com')",
			[]string{"toRecipients"},
		},
		// Words inside a literal are values, whatever they look like.
		{"literal containing operators", "subject eq 'a eq b and c'", []string{"subject"}},
		{"literal containing a field name", "subject eq 'hasAttachments'", []string{"subject"}},
		{"numbers are not fields", "size gt 1024", []string{"size"}},
		{"timestamps are not fields", "receivedDateTime ge 2026-01-02T03:04:05Z", []string{"receivedDateTime"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterFields(tt.filter)
			if len(got) != len(tt.want) {
				t.Fatalf("filterFields(%q) = %v, want %v", tt.filter, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("filterFields(%q) = %v, want %v", tt.filter, got, tt.want)
				}
			}
		})
	}
}

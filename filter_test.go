package msgraph

import (
	"net/url"
	"testing"
	"time"
)

func TestExprRendering(t *testing.T) {
	sent := time.Date(2026, 8, 17, 12, 30, 0, 0, time.UTC)

	tests := []struct {
		name string
		expr Expr
		want string
	}{
		{"eq string", Eq("subject", "hello"), "subject eq 'hello'"},
		{"eq bool", Eq("isRead", false), "isRead eq false"},
		{"eq int", Eq("importance", 3), "importance eq 3"},
		{"eq float", Eq("size", 1.5), "size eq 1.5"},
		{"ne", Ne("subject", "x"), "subject ne 'x'"},
		{"gt", Gt("size", 10), "size gt 10"},
		{"ge time", Ge("receivedDateTime", sent), "receivedDateTime ge 2026-08-17T12:30:00Z"},
		{"lt", Lt("size", 10), "size lt 10"},
		{"le", Le("size", 10), "size le 10"},
		{"is null", IsNull("parentFolderId"), "parentFolderId eq null"},
		{"is not null", IsNotNull("parentFolderId"), "parentFolderId ne null"},
		{"contains", Contains("subject", "invoice"), "contains(subject,'invoice')"},
		{"startswith", StartsWith("subject", "RE:"), "startswith(subject,'RE:')"},
		{"endswith", EndsWith("subject", "!"), "endswith(subject,'!')"},
		{"in", In("importance", "high", "normal"), "importance in ('high','normal')"},
		{"not", Not(Eq("isRead", true)), "not (isRead eq true)"},
		{
			"and",
			And(Eq("isRead", false), Ge("receivedDateTime", sent)),
			"(isRead eq false and receivedDateTime ge 2026-08-17T12:30:00Z)",
		},
		{
			"or",
			Or(Eq("importance", "high"), Eq("hasAttachments", true)),
			"(importance eq 'high' or hasAttachments eq true)",
		},
		{
			"nested",
			And(Eq("isRead", false), Or(Contains("subject", "a"), Contains("subject", "b"))),
			"(isRead eq false and (contains(subject,'a') or contains(subject,'b')))",
		},
		{
			"field path",
			Eq(Field("from", "emailAddress", "address"), "a@b.com"),
			"from/emailAddress/address eq 'a@b.com'",
		},
		{
			"any lambda",
			Any("toRecipients", "r", Eq("r/emailAddress/address", "a@b.com")),
			"toRecipients/any(r:r/emailAddress/address eq 'a@b.com')",
		},
		{
			"all lambda",
			All("toRecipients", "r", Ne("r/emailAddress/address", "a@b.com")),
			"toRecipients/all(r:r/emailAddress/address ne 'a@b.com')",
		},
		{"raw", Raw("foo eq 'bar'"), "foo eq 'bar'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.expr.ODataFilter(); got != tt.want {
				t.Fatalf("ODataFilter() = %q, want %q", got, tt.want)
			}
		})
	}
}

// An apostrophe in a value must not be able to close the OData string literal
// and inject operators into the filter.
func TestExprEscapesEmbeddedQuotes(t *testing.T) {
	expr := Eq("displayName", "O'Brien")
	if got, want := expr.ODataFilter(), "displayName eq 'O''Brien'"; got != want {
		t.Fatalf("ODataFilter() = %q, want %q", got, want)
	}

	injection := Eq("subject", "x' or isRead eq true or subject eq 'y")
	if got, want := injection.ODataFilter(),
		"subject eq 'x'' or isRead eq true or subject eq ''y'"; got != want {
		t.Fatalf("ODataFilter() = %q, want %q", got, want)
	}
}

// Floats must render at full precision. A fixed precision would quietly round
// the value and change which records the filter matches.
func TestLiteralFloatPrecision(t *testing.T) {
	tests := []struct {
		name string
		expr Expr
		want string
	}{
		{"fraction needing two digits", Eq("size", 1.25), "size eq 1.25"},
		{"long fraction", Eq("size", 0.1234567), "size eq 0.1234567"},
		{"whole number", Eq("size", 2.0), "size eq 2"},
		{"float32", Eq("size", float32(1.25)), "size eq 1.25"},
		{"negative", Eq("size", -3.5), "size eq -3.5"},
		{"large int64", Eq("size", int64(9007199254740993)), "size eq 9007199254740993"},
		{"uint64 max", Eq("size", uint64(18446744073709551615)), "size eq 18446744073709551615"},
		{"negative int", Eq("size", -7), "size eq -7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.expr.ODataFilter(); got != tt.want {
				t.Fatalf("ODataFilter() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExprTimeNormalizedToUTC(t *testing.T) {
	zone := time.FixedZone("UTC-7", -7*60*60)
	local := time.Date(2026, 8, 17, 5, 30, 0, 0, zone)
	if got, want := Ge("receivedDateTime", local).ODataFilter(),
		"receivedDateTime ge 2026-08-17T12:30:00Z"; got != want {
		t.Fatalf("ODataFilter() = %q, want %q", got, want)
	}
}

func TestCombinatorsDropNilOperands(t *testing.T) {
	if got := And(); got != nil {
		t.Fatalf("And() = %v, want nil", got)
	}
	if got := Or(nil, nil); got != nil {
		t.Fatalf("Or(nil, nil) = %v, want nil", got)
	}
	if got := Not(nil); got != nil {
		t.Fatalf("Not(nil) = %v, want nil", got)
	}
	if got := Any("toRecipients", "r", nil); got != nil {
		t.Fatalf("Any with nil predicate = %v, want nil", got)
	}
	if got := In[string]("importance"); got != nil {
		t.Fatalf("In with no values = %v, want nil", got)
	}
	if got := Raw("   "); got != nil {
		t.Fatalf("Raw(blank) = %v, want nil", got)
	}

	// A single surviving operand is emitted without redundant parentheses.
	if got, want := And(nil, Eq("isRead", true), nil).ODataFilter(), "isRead eq true"; got != want {
		t.Fatalf("And with one operand = %q, want %q", got, want)
	}
}

func TestOrderRendering(t *testing.T) {
	if got, want := Asc("subject").String(), "subject"; got != want {
		t.Fatalf("Asc = %q, want %q", got, want)
	}
	if got, want := Desc("receivedDateTime").String(), "receivedDateTime desc"; got != want {
		t.Fatalf("Desc = %q, want %q", got, want)
	}
}

func TestParamsTypedFilterAndSort(t *testing.T) {
	params := Params{
		Where: And(Eq("isRead", false), Contains("subject", "O'Brien")),
		Sort:  []Order{Desc("receivedDateTime"), Asc("subject")},
		Count: true,
	}
	values := params.Values()

	wantFilter := "(isRead eq false and contains(subject,'O''Brien'))"
	if got := values.Get("$filter"); got != wantFilter {
		t.Fatalf("$filter = %q, want %q", got, wantFilter)
	}
	if got, want := values.Get("$orderby"), "receivedDateTime desc,subject"; got != want {
		t.Fatalf("$orderby = %q, want %q", got, want)
	}
	if got, want := values.Get("$count"), "true"; got != want {
		t.Fatalf("$count = %q, want %q", got, want)
	}
}

func TestParamsCombinesTypedAndRawFilter(t *testing.T) {
	params := Params{
		Where:  Eq("isRead", false),
		Filter: "hasAttachments eq true",
	}
	want := "(isRead eq false) and (hasAttachments eq true)"
	if got := params.Values().Get("$filter"); got != want {
		t.Fatalf("$filter = %q, want %q", got, want)
	}
}

func TestParamsOmitsEmptyFilter(t *testing.T) {
	values := Params{Where: And()}.Values()
	if _, ok := values["$filter"]; ok {
		t.Fatalf("$filter present for empty Where: %v", values)
	}
}

func TestParamsSkipTokenAndRawOrderBy(t *testing.T) {
	params := Params{
		Sort:      []Order{Desc("receivedDateTime")},
		OrderBy:   []string{"", "importance asc"},
		SkipToken: "abc123",
	}
	values := params.Values()
	if got, want := values.Get("$orderby"), "receivedDateTime desc,importance asc"; got != want {
		t.Fatalf("$orderby = %q, want %q", got, want)
	}
	if got, want := values.Get("$skiptoken"), "abc123"; got != want {
		t.Fatalf("$skiptoken = %q, want %q", got, want)
	}
}

func TestParamsValidate(t *testing.T) {
	if err := (Params{Top: 10, Skip: 5}).Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if err := (Params{Top: -1}).Validate(); err == nil {
		t.Fatal("Validate() = nil for negative Top")
	}
	if err := (Params{Skip: -1}).Validate(); err == nil {
		t.Fatal("Validate() = nil for negative Skip")
	}
}

// The rendered filter has to survive query encoding intact.
func TestParamsFilterSurvivesURLEncoding(t *testing.T) {
	params := Params{Where: Eq("displayName", "O'Brien & Sons")}
	encoded := params.Values().Encode()
	parsed, err := url.ParseQuery(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := parsed.Get("$filter"), "displayName eq 'O''Brien & Sons'"; got != want {
		t.Fatalf("round-tripped $filter = %q, want %q", got, want)
	}
}

// A filter must be able to report which properties it touches without anyone
// parsing the rendered string back apart. String scanning produced nonsense
// tokens for lambdas and for literals containing spaces.
func TestFieldsOf(t *testing.T) {
	sent := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		expr Expr
		want []string
	}{
		{"nil", nil, nil},
		{"comparison", Eq("isRead", false), []string{"isRead"}},
		{"function", Contains("subject", "a b c"), []string{"subject"}},
		{"literal with spaces", Eq("subject", "hello world and eq"), []string{"subject"}},
		{"in", In("importance", "high", "low"), []string{"importance"}},
		{"not", Not(Eq("isRead", true)), []string{"isRead"}},
		{
			"and",
			And(Eq("isRead", false), Ge("receivedDateTime", sent)),
			[]string{"isRead", "receivedDateTime"},
		},
		{
			"nested",
			And(Eq("isRead", false), Or(Contains("subject", "x"), Eq("importance", "high"))),
			[]string{"isRead", "subject", "importance"},
		},
		{"duplicates collapse", And(Eq("isRead", false), Eq("isRead", true)), []string{"isRead"}},
		// The lambda reports the collection, not the bound variable: c is not a
		// property of the resource.
		{
			"lambda reports the collection",
			Any("categories", "c", Eq("c", "Red Team")),
			[]string{"categories"},
		},
		{"path field", Eq(Field("from", "emailAddress", "address"), "a@b.com"), []string{"from/emailAddress/address"}},
		{"raw is opaque", Raw("subject eq 'x'"), nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FieldsOf(tt.expr)
			if len(got) != len(tt.want) {
				t.Fatalf("FieldsOf = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("FieldsOf = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestContainsRaw(t *testing.T) {
	tests := []struct {
		name string
		expr Expr
		want bool
	}{
		{"nil", nil, false},
		{"plain", Eq("isRead", true), false},
		{"raw", Raw("x eq 1"), true},
		{"raw nested in and", And(Eq("isRead", true), Raw("x eq 1")), true},
		{"raw under not", Not(Raw("x eq 1")), true},
		{"raw in lambda", Any("categories", "c", Raw("c eq 'x'")), true},
		{"no raw nested", And(Eq("a", 1), Or(Eq("b", 2), Not(Eq("c", 3)))), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContainsRaw(tt.expr); got != tt.want {
				t.Fatalf("ContainsRaw = %v, want %v", got, tt.want)
			}
		})
	}
}

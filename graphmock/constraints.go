package graphmock

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	msgraph "github.com/albertocavalcante/msgraph-go"
)

// A Constraint inspects a request and returns a rejection, or nil to let it
// through. Constraints exist so the fake refuses what the real service refuses:
// every one here was established by running the request against a live mailbox
// and reading the error back.
type Constraint func(*Request) *Response

// ExchangeMailPageCap is the largest page Exchange will return for mail,
// whatever odata.maxpagesize asks for.
const ExchangeMailPageCap = 512

// ExchangeMailDeltaDefaultPageSize is how many messages a delta query returns
// when no page size is requested. It is small enough that walking a large
// mailbox without asking for more takes tens of thousands of requests.
const ExchangeMailDeltaDefaultPageSize = 10

// ExchangeMailSortableFields are the message properties Exchange will filter on
// while also sorting. Filtering on anything else alongside an $orderby is
// refused, because no index serves both at once.
var ExchangeMailSortableFields = []string{
	"isRead",
	"isDraft",
	"receivedDateTime",
	"sentDateTime",
	"categories",
}

// ExchangeMail returns the constraints a real Outlook mailbox applies.
//
// Using these turns a class of production-only failure into a test failure:
// a filter Exchange cannot serve is rejected here with the same error code.
func ExchangeMail() []Constraint {
	return []Constraint{
		RejectUnsupportedFilterFunctions(),
		RejectRecipientLambdas(),
		RequireSortableFilter(ExchangeMailSortableFields...),
	}
}

// RejectUnsupportedFilterFunctions refuses filter functions Exchange does not
// implement. endswith is the notable one: it is valid OData and valid on other
// Graph resources, but on mail it answers ErrorInvalidUrlQueryFilter, so a
// "sender is at this domain" filter can never work.
func RejectUnsupportedFilterFunctions(extra ...string) Constraint {
	unsupported := append([]string{"endswith("}, extra...)
	return func(r *Request) *Response {
		filter := strings.ToLower(r.Query.Filter)
		for _, function := range unsupported {
			if strings.Contains(filter, strings.ToLower(function)) {
				return reject(http.StatusBadRequest, "ErrorInvalidUrlQueryFilter",
					"The query filter contains one or more invalid nodes.")
			}
		}
		return nil
	}
}

// RejectRecipientLambdas refuses any/all over a recipient collection, which
// Exchange answers with ErrorInvalidUrlQueryFilter however it is written.
func RejectRecipientLambdas() Constraint {
	collections := []string{"torecipients/", "ccrecipients/", "bccrecipients/"}
	return func(r *Request) *Response {
		filter := strings.ToLower(r.Query.Filter)
		for _, collection := range collections {
			if strings.Contains(filter, collection+"any(") || strings.Contains(filter, collection+"all(") {
				return reject(http.StatusBadRequest, "ErrorInvalidUrlQueryFilter",
					"The query filter contains one or more invalid nodes.")
			}
		}
		return nil
	}
}

// RequireSortableFilter refuses a request that both sorts and filters on a
// property outside the sortable set, the way Exchange answers
// InefficientFilter.
//
// This is the constraint that broke most of the CLI's filters in production
// while every test passed: the default newest-first ordering meant almost any
// filter was paired with a sort.
func RequireSortableFilter(sortable ...string) Constraint {
	allowed := make(map[string]bool, len(sortable))
	for _, field := range sortable {
		allowed[strings.ToLower(field)] = true
	}
	return func(r *Request) *Response {
		if r.Query.Filter == "" || len(r.Query.OrderBy) == 0 {
			return nil
		}
		for _, field := range filterFields(r.Query.Filter) {
			if !allowed[strings.ToLower(field)] {
				return reject(http.StatusBadRequest, "InefficientFilter",
					"The restriction or sort order is too complex for this operation.")
			}
		}
		return nil
	}
}

// CapPageSize truncates an oversized odata.maxpagesize request rather than
// failing it, which is what Graph does.
func CapPageSize(maxSize int) Constraint {
	return func(r *Request) *Response {
		if size, ok := r.PageSize(); ok && size > maxSize {
			// Not a rejection: the service silently returns fewer items. The
			// handler sees the capped value through EffectivePageSize.
			r.Header.Set("Prefer", "odata.maxpagesize="+strconv.Itoa(maxSize))
		}
		return nil
	}
}

// EffectivePageSize is the number of items a request should yield, applying
// the service's default and cap. Handlers use it to page realistically.
func (r *Request) EffectivePageSize(defaultSize, maxSize int) int {
	size, ok := r.PageSize()
	if !ok || size <= 0 {
		size = defaultSize
	}
	if maxSize > 0 && size > maxSize {
		size = maxSize
	}
	return size
}

// RequireHeader refuses a request that does not carry a header, for asserting
// that a client always sends one.
func RequireHeader(name, code, message string) Constraint {
	return func(r *Request) *Response {
		if r.Header.Get(name) == "" {
			return reject(http.StatusBadRequest, code, message)
		}
		return nil
	}
}

// RequireAuthorization refuses a request without a bearer token, which is the
// single most embarrassing thing for a client to get wrong.
func RequireAuthorization() Constraint {
	return func(r *Request) *Response {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			return reject(http.StatusUnauthorized, "InvalidAuthenticationToken",
				"Access token is empty.")
		}
		return nil
	}
}

func reject(status int, code, message string) *Response {
	response := Fault(status, code, message)
	return &response
}

// filterFields pulls property paths out of a rendered filter. The mock cannot
// use the typed tree, because by the time a filter reaches the wire it is a
// string; this scan is deliberately generous, treating anything that is not an
// operator or a literal as a property, so the constraint errs towards
// rejecting rather than towards quietly permitting.
func filterFields(filter string) []string {
	var fields []string
	var token strings.Builder
	inLiteral := false
	// lambdaDepth counts how deep we are inside a collection lambda's body.
	// Everything in there is bound to the lambda variable and is not a
	// property of the resource, so emitting it would report "c" as a field.
	lambdaDepth, parens := 0, 0

	flush := func() {
		word := token.String()
		token.Reset()
		if word == "" || lambdaDepth > 0 || isOperator(word) || isLiteralish(word) {
			return
		}
		fields = append(fields, word)
	}

	runes := []rune(filter)
	for i := range runes {
		r := runes[i]
		switch {
		case r == '\'':
			inLiteral = !inLiteral
		case inLiteral:
			// Literal contents are values, never properties.
		case r == '(':
			word := token.String()
			// A lambda renders as collection/any(variable:predicate). Emit the
			// collection, then treat the body as opaque.
			if collection, ok := lambdaCollection(word); ok {
				token.Reset()
				if lambdaDepth == 0 {
					fields = append(fields, collection)
				}
				lambdaDepth++
			} else {
				flush()
			}
			parens++
		case r == ')':
			flush()
			parens--
			if lambdaDepth > 0 && parens < lambdaDepth {
				lambdaDepth--
			}
		case r == ' ' || r == ',' || r == ':':
			flush()
		default:
			token.WriteRune(r)
		}
	}
	flush()
	return fields
}

// lambdaCollection reports the collection in a "collection/any" or
// "collection/all" token.
func lambdaCollection(word string) (string, bool) {
	for _, suffix := range []string{"/any", "/all"} {
		if collection, ok := strings.CutSuffix(word, suffix); ok && collection != "" {
			return collection, true
		}
	}
	return "", false
}

func isOperator(word string) bool {
	switch strings.ToLower(word) {
	case "eq", "ne", "gt", "ge", "lt", "le", "and", "or", "not", "in",
		"null", "true", "false", "contains", "startswith", "endswith":
		return true
	}
	return false
}

// isLiteralish reports whether a token looks like a number or a timestamp
// rather than a property name.
func isLiteralish(word string) bool {
	for _, r := range word {
		if (r < '0' || r > '9') && !strings.ContainsRune("+-.:TZ", r) {
			return false
		}
	}
	return true
}

// RequireScopes refuses a request whose resource needs a permission the token
// was not granted, the way the service does.
//
// This exists because of a bug that reached a real mailbox. A token is issued
// for the scopes a client *requests*, not for everything the user has
// consented to. Every command asked for the default mail scopes, so the
// mailbox settings commands were refused even though consent covered them --
// and the error told the user to grant a permission they already held. A fake
// that answered every path identically could not have caught it: the whole
// bug lived in the difference between paths.
//
// The refusal is deliberately as unhelpful as the real one. Graph answers a
// missing delegated permission with ErrorAccessDenied and names neither the
// permission nor the resource, so a caller that only works when the error is
// descriptive will not work in production either.
func RequireScopes(granted ...string) Constraint {
	return func(req *Request) *Response {
		suggestion := msgraph.SuggestDelegatedScopes(req.Method, req.Path)
		// An unrecognized path carries no permission claim, so let it
		// through: guessing here would fail tests for routes this table
		// does not know about yet.
		if len(suggestion.Scopes) == 0 {
			return nil
		}
		// Any one of the suggested permissions is enough. They are
		// alternatives -- read or read-write -- not a required set.
		for _, scope := range suggestion.Scopes {
			if msgraph.ScopeSatisfies(granted, scope) {
				return nil
			}
		}
		return reject(http.StatusForbidden, "ErrorAccessDenied",
			"Access is denied. Check credentials and try again.")
	}
}

// ExchangeCalendarPageCap is the largest page Exchange returns for a calendar
// view, whatever odata.maxpagesize asks for.
const ExchangeCalendarPageCap = 1000

// ExchangeCalendar returns the constraints a real Outlook calendar applies.
//
// The calendar rules differ from the mail ones in kind, not just in detail: a
// calendar view is a distinct resource with required parameters rather than a
// filtered collection, and getting that wrong produces an empty or wrong
// answer rather than an error.
func ExchangeCalendar() []Constraint {
	return []Constraint{
		RequireCalendarWindow(),
		RejectFilterOnCalendarView(),
	}
}

// RequireCalendarWindow refuses a calendar view that does not name the range
// it wants.
//
// The service treats startDateTime and endDateTime as required parameters of
// the resource, not as an optional filter, and answers a request without them
// with ErrorInvalidArgument.
func RequireCalendarWindow() Constraint {
	return func(r *Request) *Response {
		if !strings.Contains(strings.ToLower(r.Path), "/calendarview") {
			return nil
		}
		start, end := r.Query.Raw.Get("startDateTime"), r.Query.Raw.Get("endDateTime")
		if start == "" || end == "" {
			return reject(http.StatusBadRequest, "ErrorInvalidArgument",
				"The startDateTime and endDateTime parameters are required for a calendar view.")
		}
		// A backwards range is accepted by the service and answers nothing,
		// which reads as a free afternoon rather than as a mistake.
		if start > end {
			return reject(http.StatusBadRequest, "ErrorInvalidArgument",
				"startDateTime must be earlier than endDateTime.")
		}
		return nil
	}
}

// RejectFilterOnCalendarView refuses a $filter on start or end in a calendar
// view.
//
// The window is what selects events; expressing it as a filter as well is a
// misunderstanding the service does not correct, because it silently ignores
// the filter and returns the whole window.
func RejectFilterOnCalendarView() Constraint {
	return func(r *Request) *Response {
		if !strings.Contains(strings.ToLower(r.Path), "/calendarview") {
			return nil
		}
		filter := strings.ToLower(r.Query.Filter)
		if strings.Contains(filter, "start/") || strings.Contains(filter, "end/") {
			return reject(http.StatusBadRequest, "ErrorInvalidUrlQueryFilter",
				"A calendar view is bounded by startDateTime and endDateTime, not by a filter on start or end.")
		}
		return nil
	}
}

// ExchangeOutlook returns the constraints that apply across an Outlook
// mailbox, beyond the mail and calendar sets.
//
// Each one here was a bug before it was a constraint. The service does not
// refuse these in a way anyone would guess, so a fake that answered
// optimistically let the mistake through and the suite stayed green.
func ExchangeOutlook() []Constraint {
	return []Constraint{
		RequireRuleSequence(),
		RequireAllDayAtMidnight(),
	}
}

// RequireRuleSequence refuses a new inbox rule that does not say where it runs
// in the order.
//
// The service requires a positive sequence and will not choose one. It answers
// a rule without it with a MessageRuleValidationError naming the field and not
// the remedy, so every rule created with a default-constructed body failed --
// which is every rule, until something tried it against a real mailbox.
func RequireRuleSequence() Constraint {
	return func(r *Request) *Response {
		if r.Method != http.MethodPost || !strings.Contains(strings.ToLower(r.Path), "messagerules") {
			return nil
		}
		var rule struct {
			Sequence *int32 `json:"sequence"`
		}
		if err := json.Unmarshal(r.Body, &rule); err != nil {
			return nil
		}
		if rule.Sequence == nil || *rule.Sequence <= 0 {
			return reject(http.StatusBadRequest, "MessageRuleValidationError",
				"ErrorCode: 'InvalidValue', Message: 'The value isn't valid.', Field: 'Sequence'.")
		}
		return nil
	}
}

// RequireAllDayAtMidnight refuses an all-day event whose ends are not midnight.
//
// An all-day event is a pair of calendar dates rather than instants, and the
// service enforces that by requiring both ends at midnight. It reports the
// refusal without naming which end was wrong, and a client reading dates in a
// local zone produces exactly this shape without noticing.
func RequireAllDayAtMidnight() Constraint {
	return func(r *Request) *Response {
		if r.Method != http.MethodPost && r.Method != http.MethodPatch {
			return nil
		}
		if !strings.Contains(strings.ToLower(r.Path), "/events") {
			return nil
		}
		var event struct {
			IsAllDay bool `json:"isAllDay"`
			Start    *struct {
				DateTime string `json:"dateTime"`
			} `json:"start"`
			End *struct {
				DateTime string `json:"dateTime"`
			} `json:"end"`
		}
		if err := json.Unmarshal(r.Body, &event); err != nil || !event.IsAllDay {
			return nil
		}
		for _, end := range []*struct {
			DateTime string `json:"dateTime"`
		}{event.Start, event.End} {
			if end == nil {
				continue
			}
			if !atMidnight(end.DateTime) {
				return reject(http.StatusBadRequest, "ErrorInvalidTimeForEvent",
					"The start and end times of an all-day event must be midnight.")
			}
		}
		return nil
	}
}

// atMidnight reports whether a Graph wall-clock reading sits at midnight. The
// zone half is irrelevant: the service checks the clock it was sent, which is
// the whole reason a client converting to a local zone trips this.
func atMidnight(dateTime string) bool {
	if dateTime == "" {
		return false
	}
	_, clock, hasClock := strings.Cut(dateTime, "T")
	if !hasClock {
		// A bare date carries no clock, so it is midnight by definition.
		return true
	}
	clock = strings.TrimSuffix(clock, "Z")

	// The service varies the fractional precision, so any number of trailing
	// zeros counts as none.
	if whole, fraction, hasFraction := strings.Cut(clock, "."); hasFraction {
		if strings.Trim(fraction, "0") != "" {
			return false
		}
		clock = whole
	}
	return clock == "00:00:00" || clock == "00:00"
}

// RejectStaleETag refuses a conditional write whose If-Match does not name the
// current version.
//
// Exchange answers a stale mail tag with 400 and ErrorInvalidChangeKey, not
// the 412 the header implies, so the obvious precondition check misses it and
// the write looks like an ordinary bad request.
func RejectStaleETag(current string) Constraint {
	return func(r *Request) *Response {
		match := r.Header.Get("If-Match")
		if match == "" || match == "*" || match == current {
			return nil
		}
		return reject(http.StatusBadRequest, "ErrorInvalidChangeKey",
			"The specified change key is invalid.")
	}
}

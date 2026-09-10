package graphmock

import (
	"net/http"
	"strconv"
	"strings"
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

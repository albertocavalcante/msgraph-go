package graphmock

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Page and expansion limits the Teams endpoints apply.
//
// None of these is announced. Every one is a number the service silently
// substitutes for whatever was asked, so a caller that gets exactly this many
// items cannot tell a truncated answer from a complete one.
const (
	// TeamsChatPageCap is the largest page the chat listing serves.
	TeamsChatPageCap = 50
	// TeamsMessagePageCap is the largest page a chat or channel message
	// listing serves.
	TeamsMessagePageCap = 50
	// TeamsChannelMessageDefaultPageSize is what a channel message listing
	// returns when $top says nothing, which is well under the cap.
	TeamsChannelMessageDefaultPageSize = 20
	// TeamsExpandedMemberCap is how many members $expand=members returns,
	// however many the chat has and whatever $top asks for. There is no
	// continuation link for the rest.
	TeamsExpandedMemberCap = 25
	// TeamsExpandedReplyCap is how many replies $expand=replies returns
	// before paging the remainder through a per-message link.
	TeamsExpandedReplyCap = 1000
	// TeamsSearchPageCap is the largest message search page.
	TeamsSearchPageCap = 25
)

// TeamsMessageSortableFields are the only two properties a message listing
// will order by. Both are timestamps, and neither can be ordered ascending.
var TeamsMessageSortableFields = []string{"createdDateTime", "lastModifiedDateTime"}

// Teams returns the constraints a real Teams tenant applies.
//
// It is worth reading alongside [ExchangeMail], because the two fail in
// opposite ways. Exchange refuses what it cannot serve, so the Exchange bundle
// is constraints: a bad request comes back as an error a test can see. Teams
// mostly answers anyway. An unservable filter is dropped, an oversized page is
// quietly trimmed, an enum the request did not opt into is replaced with a
// placeholder -- all with 200 OK and a body that parses.
//
// That is why the Teams half of this fake lives mostly in [TeamsShapers], and
// why using these constraints without those shapers reproduces the easy
// failures and none of the dangerous ones.
func Teams() []Constraint {
	return []Constraint{
		IgnoreMismatchedMessageFilter(),
		RejectAscendingMessageSort(),
		RejectUnsupportedMessageSortProperty(TeamsMessageSortableFields...),
		RejectSelectOnMessages(),
		CapTop("/chats", TeamsChatPageCap),
		CapTop("/messages", TeamsMessagePageCap),
		CapTop("/search/query", TeamsSearchPageCap),
		RejectSearchSort(),
		RequireSearchEntityTypes("chatMessage"),
	}
}

// IgnoreMismatchedMessageFilter drops a $filter that names a property the
// $orderby does not, the way a Teams message listing does.
//
// This is the most dangerous behavior in the Teams API and the reason this
// bundle exists. The service does not refuse the request and does not warn:
// it discards the filter and answers the whole collection, successfully. A
// caller asking for the last day of a chat gets a year of it and renders the
// lot as today. A filter with no $orderby at all is discarded the same way.
//
// So this drops the filter rather than rejecting the request. Rejecting would
// be easier to test against and would be fiction -- a failure mode production
// never exhibits -- and a client written against a fake that refuses would
// still be wrong against the service that does not.
func IgnoreMismatchedMessageFilter() Constraint {
	return func(r *Request) *Response {
		if !isMessagePath(r.Path) || r.Query.Filter == "" {
			return nil
		}
		ordered := orderByProperty(r.Query.OrderBy)
		if ordered != "" && filterNamesOnly(r.Query.Filter, ordered) {
			return nil
		}
		// Not a rejection. The handler sees no filter and answers everything,
		// which is exactly what the caller gets in production.
		r.Query.Filter = ""
		r.Query.Raw.Del("$filter")
		return nil
	}
}

// RejectAscendingMessageSort refuses an ascending order on a message listing.
//
// Descending is the only direction served. This one the service does refuse,
// so a caller wanting oldest-first has to page the whole range and reverse it
// -- worth discovering in a test rather than after building a transcript view
// around an ordering that does not exist.
func RejectAscendingMessageSort() Constraint {
	return func(r *Request) *Response {
		if !isMessagePath(r.Path) {
			return nil
		}
		for _, clause := range r.Query.OrderBy {
			if !strings.Contains(strings.ToLower(clause), " desc") {
				return reject(http.StatusBadRequest, "BadRequest",
					"Only descending order is supported for this collection.")
			}
		}
		return nil
	}
}

// RejectUnsupportedMessageSortProperty refuses an $orderby on anything but the
// two timestamps a message listing indexes.
func RejectUnsupportedMessageSortProperty(sortable ...string) Constraint {
	allowed := make(map[string]bool, len(sortable))
	for _, field := range sortable {
		allowed[strings.ToLower(field)] = true
	}
	return func(r *Request) *Response {
		if !isMessagePath(r.Path) {
			return nil
		}
		for _, clause := range r.Query.OrderBy {
			property := strings.ToLower(orderByProperty([]string{clause}))
			if property != "" && !allowed[property] {
				return reject(http.StatusBadRequest, "BadRequest",
					"The property is not supported for ordering on this collection.")
			}
		}
		return nil
	}
}

// RejectSelectOnMessages refuses $select and $expand on a message listing.
//
// Unlike the filter, these are genuinely refused, which is the distinction
// worth encoding: a caller may reasonably expect $select to trim a chat
// message and it returns 400 instead. The one exception is expanding replies
// on a channel message collection, which is supported and is the only way to
// read a thread in one request.
func RejectSelectOnMessages() Constraint {
	return func(r *Request) *Response {
		if !isMessagePath(r.Path) {
			return nil
		}
		if len(r.Query.Select) > 0 {
			return reject(http.StatusBadRequest, "BadRequest",
				"The $select query option is not supported for this collection.")
		}
		for _, expand := range r.Query.Raw["$expand"] {
			if strings.EqualFold(strings.TrimSpace(expand), "replies") && isChannelMessagePath(r.Path) {
				continue
			}
			return reject(http.StatusBadRequest, "BadRequest",
				"The $expand query option is not supported for this collection.")
		}
		return nil
	}
}

// CapTop clamps an oversized $top on any path containing the fragment, the way
// Teams does.
//
// It is a clamp and not a rejection because the service serves fewer items
// without comment. A caller that asked for two hundred and received fifty has
// no way to distinguish that from a chat holding fifty messages, which is why
// a client needs to be told the number it asked for was not honored rather
// than inferring completeness from a short page.
func CapTop(pathFragment string, maxTop int) Constraint {
	return func(r *Request) *Response {
		if !strings.Contains(strings.ToLower(r.Path), strings.ToLower(pathFragment)) {
			return nil
		}
		if r.Query.Top <= maxTop {
			return nil
		}
		r.Query.Top = maxTop
		r.Query.Raw.Set("$top", strconv.Itoa(maxTop))
		return nil
	}
}

// RejectSearchSort refuses sortProperties on a chatMessage search.
//
// Message hits come back in relevance order and nothing can change that. A
// caller building a chronological view from search results has to sort them
// itself, and finding that out here is cheaper than finding it out after the
// view exists.
func RejectSearchSort() Constraint {
	return func(r *Request) *Response {
		if !isSearchPath(r.Path) {
			return nil
		}
		requests, ok := searchRequests(r.Body)
		if !ok {
			return nil
		}
		for _, request := range requests {
			if _, sorted := request["sortProperties"]; sorted && searchWants(request, "chatMessage") {
				return reject(http.StatusBadRequest, "invalidRequest",
					"SortProperties is not supported for the requested entity types.")
			}
		}
		return nil
	}
}

// RequireSearchEntityTypes refuses a search body that does not name one of the
// entity types, so a mis-built request fails here instead of succeeding with
// an empty-looking result that reads as "nothing matched".
func RequireSearchEntityTypes(required ...string) Constraint {
	return func(r *Request) *Response {
		if !isSearchPath(r.Path) {
			return nil
		}
		requests, ok := searchRequests(r.Body)
		if !ok {
			return nil
		}
		for _, request := range requests {
			for _, want := range required {
				if searchWants(request, want) {
					return nil
				}
			}
		}
		return reject(http.StatusBadRequest, "invalidRequest",
			"The entityTypes property is required and must name a supported type.")
	}
}

// isMessagePath reports whether the path addresses a chat or channel message
// collection or one of its members.
func isMessagePath(path string) bool {
	return isChatMessagePath(path) || isChannelMessagePath(path)
}

func isChatMessagePath(path string) bool {
	lowered := strings.ToLower(path)
	return strings.Contains(lowered, "/chats/") && strings.Contains(lowered, "/messages")
}

func isChannelMessagePath(path string) bool {
	lowered := strings.ToLower(path)
	return strings.Contains(lowered, "/channels/") && strings.Contains(lowered, "/messages")
}

func isSearchPath(path string) bool {
	return strings.Contains(strings.ToLower(path), "/search/query")
}

// orderByProperty reads the property out of the first $orderby clause,
// discarding the direction.
func orderByProperty(orderBy []string) string {
	if len(orderBy) == 0 {
		return ""
	}
	clause := strings.TrimSpace(orderBy[0])
	if clause == "" {
		return ""
	}
	if space := strings.IndexByte(clause, ' '); space >= 0 {
		clause = clause[:space]
	}
	return clause
}

// filterNamesOnly reports whether every property the filter touches is the
// named one. A filter over two properties is as unservable as a filter over
// the wrong one, and is discarded the same way.
func filterNamesOnly(filter, property string) bool {
	fields := filterFields(filter)
	if len(fields) == 0 {
		return false
	}
	for _, field := range fields {
		if !strings.EqualFold(field, property) {
			return false
		}
	}
	return true
}

// searchRequests pulls the requests array out of a /search/query body.
func searchRequests(body []byte) ([]map[string]any, bool) {
	if len(body) == 0 {
		return nil, false
	}
	var payload struct {
		Requests []map[string]any `json:"requests"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false
	}
	return payload.Requests, len(payload.Requests) > 0
}

// searchWants reports whether one search request names the entity type.
func searchWants(request map[string]any, entityType string) bool {
	types, ok := request["entityTypes"].([]any)
	if !ok {
		return false
	}
	for _, value := range types {
		if name, isString := value.(string); isString && strings.EqualFold(name, entityType) {
			return true
		}
	}
	return false
}

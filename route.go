package msgraph

import "strings"

const (
	// routePlaceholder stands in for every path segment that is not a known
	// fixed Graph segment in its position.
	routePlaceholder = "{id}"
	// maxRouteSegments bounds a rendered route; anything longer ends in
	// routeTruncated.
	maxRouteSegments = 16
	routeTruncated   = "..."
)

// routeTemplate turns an escaped request path into a low-cardinality route
// for telemetry, such as "/v1.0/me/mailFolders/{id}/messages/{id}".
//
// The rule is an allowlist, so that anything unrecognized is redacted rather
// than leaked. A segment survives only when it case-insensitively matches a
// fixed Graph segment, and the segment right after a collection (users,
// messages, lists, ...) must match that collection's own short list of
// children (delta, $count, a cast, a well-known folder for mailFolders),
// because that position holds an identifier or a user-chosen name. A call
// such as "delta()" or "users('...')" keeps an allowlisted name but never its
// arguments. Path-based addressing, from a segment ending in ":" through the
// next one ending in ":" (or the end of the path), is a single {id}.
// Consecutive {id}s collapse into one, and at most maxRouteSegments segments
// are rendered. The query string never reaches this function.
func routeTemplate(escapedPath string) string {
	var b strings.Builder
	segments := 0
	previousWasID := false
	emit := func(out string) bool {
		isID := out == routePlaceholder
		if isID && previousWasID {
			return true
		}
		if segments == maxRouteSegments {
			b.WriteString("/" + routeTruncated)
			return false
		}
		previousWasID = isID
		segments++
		b.WriteByte('/')
		b.WriteString(out)
		return true
	}
	var children map[string]string // non-nil right after a collection
	inAddress := false
	for segment := range strings.SplitSeq(escapedPath, "/") {
		switch {
		case segment == "":
			continue
		case inAddress:
			inAddress = !strings.HasSuffix(segment, ":")
			continue
		case strings.HasSuffix(segment, ":"):
			inAddress = true
			children = nil
			if !emit(routePlaceholder) {
				return b.String()
			}
			continue
		}
		var out string
		out, children = templateSegment(segment, children)
		if !emit(out) {
			return b.String()
		}
	}
	if b.Len() == 0 {
		return "/"
	}
	return b.String()
}

// templateSegment renders one segment. allowed is the set of names valid in
// this position: a collection's children, or nil for the general set. It
// returns the children for the next position.
func templateSegment(segment string, allowed map[string]string) (out string, next map[string]string) {
	name, args, isCall := strings.Cut(segment, "(")
	if isCall && !strings.HasSuffix(args, ")") {
		return routePlaceholder, nil
	}
	if allowed == nil {
		allowed = fixedSegments
	}
	canonical, ok := allowed[strings.ToLower(name)]
	switch {
	case !ok:
		return routePlaceholder, nil
	case !isCall:
		return canonical, collectionChildren[strings.ToLower(canonical)]
	case args == ")":
		return canonical + "()", nil
	default:
		// The arguments are the key, as in users('...'), so what follows is
		// a navigation rather than another identifier.
		return canonical + "(" + routePlaceholder + ")", nil
	}
}

// Adding a word to these lists makes it visible in telemetry, so add only
// fixed Graph schema names, never anything a user or tenant chooses.
var (
	// collections is followed by an identifier, so the next segment is
	// checked against the collection's children rather than fixedSegments.
	collections = []string{
		"users", "groups", "organization", "applications", "servicePrincipals",
		"directoryObjects", "directReports", "memberOf", "transitiveMemberOf",
		"members", "owners", "photos", "people", "methods", "extensions",
		"subscriptions", "messages", "mailFolders", "childFolders", "attachments",
		"messageRules", "overrides", "masterCategories",
		"singleValueExtendedProperties", "multiValueExtendedProperties", "events",
		"calendars", "calendarView", "calendarGroups", "instances", "contacts",
		"contactFolders", "places", "rooms", "roomLists", "lists", "tasks",
		"checklistItems", "linkedResources", "drives", "sites", "items",
		"children", "special", "permissions", "thumbnails", "versions",
		"worksheets", "tables", "rows", "columns", "chats", "teams", "channels",
		"replies", "joinedTeams", "allChannels", "hostedContents",
		"pinnedMessages", "tabs", "installedApps", "onlineMeetings", "callRecords",
	}
	// navigations are fixed segments that are not followed by an identifier.
	navigations = []string{
		"v1.0", "beta", "$batch", "$value", "$count", "$ref", "$metadata",
		"me", "manager", "photo", "presence", "authentication",
		"getMemberGroups", "checkMemberGroups", "getByIds", "translateExchangeIds",
		"mailboxSettings", "automaticRepliesSetting", "inferenceClassification",
		"outlook", "sendMail", "send", "reply", "replyAll", "forward",
		"createReply", "createReplyAll", "createForward", "move", "copy",
		"calendar", "accept", "decline", "tentativelyAccept", "cancel",
		"findMeetingTimes", "getSchedule", "reminderView", "todo", "drive",
		"root", "content", "invite", "createLink", "preview", "recent",
		"sharedWithMe", "followed", "search", "workbook", "range",
		"primaryChannel", "filesFolder", "getAllMessages", "setReaction",
		"unsetReaction", "softDelete", "undoSoftDelete",
		"sendActivityNotification", "communications", "delta",
	}
	// qualifiedNames are the casts and qualified functions the library and
	// the mail, calendar, directory and drive workloads use.
	qualifiedNames = []string{
		"microsoft.graph.delta", "microsoft.graph.user", "microsoft.graph.group",
		"microsoft.graph.device", "microsoft.graph.application",
		"microsoft.graph.servicePrincipal", "microsoft.graph.orgContact",
		"microsoft.graph.directoryRole", "microsoft.graph.room",
		"microsoft.graph.roomList", "microsoft.graph.eventMessage",
		"microsoft.graph.eventMessageRequest", "microsoft.graph.fileAttachment",
		"microsoft.graph.itemAttachment", "microsoft.graph.referenceAttachment",
	}
	// knownChildren lists, per collection, the fixed names that may stand
	// where its identifier would. Every collection also admits delta, $count,
	// $ref and the qualified names.
	knownChildren = map[string][]string{
		"users":       {"getByIds"},
		"groups":      {"getByIds"},
		"chats":       {"getAllMessages"},
		"channels":    {"getAllMessages"},
		"sites":       {"root"},
		"items":       {"root"},
		"mailFolders": wellKnownMailFolders,
		"special":     {"documents", "photos", "cameraRoll", "approot", "music", "recordings"},
	}
	wellKnownMailFolders = []string{
		"inbox", "drafts", "sentItems", "deletedItems", "junkEmail", "archive",
		"outbox", "clutter", "conflicts", "conversationHistory", "localFailures",
		"msgFolderRoot", "recoverableItemsDeletions", "scheduled", "searchFolders",
		"serverFailures", "syncIssues",
	}
)

// fixedSegments maps a lowercased segment to its canonical spelling, and
// collectionChildren maps a lowercased collection to the names allowed right
// after it.
var fixedSegments, collectionChildren = func() (map[string]string, map[string]map[string]string) {
	add := func(m map[string]string, names ...string) {
		for _, name := range names {
			m[strings.ToLower(name)] = name
		}
	}
	fixed := map[string]string{}
	add(fixed, collections...)
	add(fixed, navigations...)
	add(fixed, qualifiedNames...)
	children := make(map[string]map[string]string, len(collections))
	for _, collection := range collections {
		set := map[string]string{}
		add(set, "delta", "$count", "$ref")
		add(set, qualifiedNames...)
		add(set, knownChildren[collection]...)
		children[strings.ToLower(collection)] = set
	}
	return fixed, children
}()

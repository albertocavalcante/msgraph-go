package msgraph

import "strings"

// routePlaceholder stands in for every path segment that is not a known fixed
// Graph segment.
const routePlaceholder = "{id}"

// routeTemplate turns an escaped request path into a low-cardinality route
// for telemetry, such as "/v1.0/me/mailFolders/{id}/messages/{id}".
//
// The rule is an allowlist, so that anything unrecognized is redacted rather
// than leaked: a segment survives only when it case-insensitively matches a
// fixed Graph segment (an API version, a collection or navigation property,
// an action, a well-known folder name, a $-system segment) or is a
// "microsoft.graph.*" qualified name; a call such as "delta()" or
// "users('...')" keeps an allowlisted name but never its arguments; every
// other segment becomes {id}, and consecutive {id}s collapse into one so that
// path-based addressing like "root:/a/b/c.txt:" cannot grow the cardinality.
// The query string never reaches this function.
func routeTemplate(escapedPath string) string {
	var b strings.Builder
	previousWasID := false
	for segment := range strings.SplitSeq(escapedPath, "/") {
		if segment == "" {
			continue
		}
		out := templateSegment(segment)
		isID := out == routePlaceholder
		if isID && previousWasID {
			continue
		}
		previousWasID = isID
		b.WriteByte('/')
		b.WriteString(out)
	}
	if b.Len() == 0 {
		return "/"
	}
	return b.String()
}

func templateSegment(segment string) string {
	name, args, isCall := strings.Cut(segment, "(")
	if isCall && !strings.HasSuffix(args, ")") {
		return routePlaceholder
	}
	canonical, ok := fixedSegment(name)
	switch {
	case !ok:
		return routePlaceholder
	case !isCall:
		return canonical
	case args == ")":
		return canonical + "()"
	default:
		return canonical + "(" + routePlaceholder + ")"
	}
}

func fixedSegment(name string) (string, bool) {
	if canonical, ok := fixedSegments[strings.ToLower(name)]; ok {
		return canonical, true
	}
	// Type casts and qualified function names are schema, never data.
	const namespace = "microsoft.graph."
	if len(name) > len(namespace) && strings.EqualFold(name[:len(namespace)], namespace) &&
		isASCIILetters(name[len(namespace):]) {
		return namespace + name[len(namespace):], true
	}
	return "", false
}

func isASCIILetters(value string) bool {
	for i := range len(value) {
		c := value[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

// fixedSegments maps a lowercased segment to its canonical spelling. Adding a
// word here makes it visible in telemetry, so only add fixed Graph schema
// names, never anything a user or tenant chooses.
var fixedSegments = func() map[string]string {
	names := []string{
		"v1.0", "beta",
		"$batch", "$value", "$count", "$ref", "$metadata",
		// Directory.
		"me", "users", "groups", "organization", "applications", "servicePrincipals",
		"directoryObjects", "contacts", "manager", "directReports", "memberOf",
		"transitiveMemberOf", "members", "owners", "photo", "photos", "people",
		"getMemberGroups", "checkMemberGroups", "getByIds", "translateExchangeIds",
		"presence", "authentication", "methods", "extensions", "subscriptions",
		// Mail.
		"messages", "mailFolders", "childFolders", "attachments", "messageRules",
		"mailboxSettings", "automaticRepliesSetting", "inferenceClassification",
		"overrides", "outlook", "masterCategories", "sendMail", "send", "reply",
		"replyAll", "forward", "createReply", "createReplyAll", "createForward",
		"move", "copy", "singleValueExtendedProperties", "multiValueExtendedProperties",
		// Well-known mail folder names.
		"inbox", "drafts", "sentItems", "deletedItems", "junkEmail", "archive",
		"outbox", "clutter", "conflicts", "conversationHistory", "localFailures",
		"msgFolderRoot", "recoverableItemsDeletions", "scheduled", "searchFolders",
		"serverFailures", "syncIssues",
		// Calendar and contacts.
		"events", "calendar", "calendars", "calendarView", "calendarGroups",
		"instances", "contactFolders", "accept", "decline", "tentativelyAccept",
		"cancel", "findMeetingTimes", "getSchedule", "reminderView", "places",
		"rooms", "roomLists",
		// Tasks.
		"todo", "lists", "tasks", "checklistItems", "linkedResources",
		// Files.
		"drive", "drives", "sites", "items", "root", "children", "content",
		"special", "permissions", "invite", "createLink", "thumbnails",
		"versions", "preview", "recent", "sharedWithMe", "followed", "search",
		"workbook", "worksheets", "tables", "rows", "columns", "range",
		// Teams.
		"chats", "teams", "channels", "replies", "joinedTeams", "primaryChannel",
		"allChannels", "filesFolder", "hostedContents", "pinnedMessages", "tabs",
		"installedApps", "setReaction", "unsetReaction", "softDelete",
		"undoSoftDelete", "sendActivityNotification", "onlineMeetings",
		"communications", "callRecords",
		// Change tracking.
		"delta",
	}
	m := make(map[string]string, len(names))
	for _, name := range names {
		m[strings.ToLower(name)] = name
	}
	return m
}()

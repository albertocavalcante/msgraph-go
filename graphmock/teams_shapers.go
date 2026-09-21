package graphmock

import (
	"encoding/json"
	"strings"

	msgraph "github.com/albertocavalcante/msgraph-go"
)

// TeamsShapers returns the shapers that present a Teams answer the way the
// service presents it.
//
// These matter more than the constraints. Everything here answers 200 with a
// body that parses, and every one of them has a client-side failure that no
// amount of error handling catches: a third of a chat rendered as blank lines,
// a member list that stops at twenty-five without saying so, a thread that
// ends mid-argument. A fake that returns the convenient shape agrees with the
// client and proves nothing.
func TeamsShapers() []Shaper {
	return []Shaper{
		HideEnumsWithoutPrefer(),
		DropSenderOnSystemEvents(),
		DropTopicOnOneOnOneChats(),
		TruncateExpandedMembers(TeamsExpandedMemberCap),
		PageExpandedReplies(TeamsExpandedReplyCap),
		RootChannelMessagesOnly(),
		PopulateOnlyJoinedTeamsFields(),
	}
}

// knownMessageTypes are the message types the service will name without being
// asked. Everything else is an evolvable-enum member.
var knownMessageTypes = map[string]bool{
	"message":   true,
	"chatEvent": true,
	"typing":    true,
}

// HideEnumsWithoutPrefer replaces every messageType outside the base set with
// unknownFutureValue unless the request asked to see evolvable enum members.
//
// This is the most valuable shaper in the bundle. messageType is an evolvable
// enum, so a request that does not send Prefer: include-unknown-enum-members
// gets systemEventMessage reported as unknownFutureValue -- with no error, and
// with eventDetail still populated. In a real chat, member joins, renames and
// call notices are a large minority of the history, so a client switching on
// the type renders a substantial part of every conversation as nothing at all.
//
// It is also the only check that proves the header survives the network. A
// client can set the directive perfectly and have a proxy strip it; nothing
// else in a test suite would notice.
func HideEnumsWithoutPrefer() Shaper {
	return func(r *Request, response *Response) {
		if r != nil && r.Prefers(msgraph.PreferIncludeUnknownEnumMembers) {
			return
		}
		rewriteBody(response, func(node any) {
			object, ok := node.(map[string]any)
			if !ok {
				return
			}
			messageType, isString := object["messageType"].(string)
			if isString && !knownMessageTypes[messageType] {
				object["messageType"] = "unknownFutureValue"
			}
		})
	}
}

// DropSenderOnSystemEvents removes from wherever eventDetail is present.
//
// Nothing sent a system event message: the service generated it, and from is
// null. A fake that helpfully supplies a sender lets a client dereference it
// unconditionally and pass, then panic or render a blank name against a real
// chat on the first membership change.
func DropSenderOnSystemEvents() Shaper {
	return func(_ *Request, response *Response) {
		rewriteBody(response, func(node any) {
			object, ok := node.(map[string]any)
			if !ok {
				return
			}
			if detail, present := object["eventDetail"]; present && detail != nil {
				object["from"] = nil
			}
		})
	}
}

// DropTopicOnOneOnOneChats removes topic from every one-on-one chat.
//
// The service sends null, because a one-on-one chat has no name of its own --
// its name is the other participant, which the caller has to assemble from the
// members. A fake that supplies a topic renders a tidy chat list in tests and
// a column of blank rows in production, where most chats are one-on-one.
func DropTopicOnOneOnOneChats() Shaper {
	return func(_ *Request, response *Response) {
		rewriteBody(response, func(node any) {
			object, ok := node.(map[string]any)
			if !ok {
				return
			}
			if chatType, isString := object["chatType"].(string); isString && chatType == "oneOnOne" {
				object["topic"] = nil
			}
		})
	}
}

// TruncateExpandedMembers cuts an expanded members array to the cap and emits
// no continuation link.
//
// The absent link is the point. Everywhere else in Graph a truncated
// collection carries a nextLink, so a client that follows links everywhere is
// correct everywhere -- except here, where it silently reports a two hundred
// person chat as having twenty-five members and no indication otherwise.
func TruncateExpandedMembers(limit int) Shaper {
	return func(_ *Request, response *Response) {
		rewriteBody(response, func(node any) {
			object, ok := node.(map[string]any)
			if !ok {
				return
			}
			members, isSlice := object["members"].([]any)
			if !isSlice || len(members) <= limit {
				return
			}
			object["members"] = members[:limit]
			// Deliberately no members@odata.nextLink: the service does not
			// send one, and inventing it here would hide the whole hazard.
			delete(object, "members@odata.nextLink")
		})
	}
}

// PageExpandedReplies cuts an expanded replies array to the cap and injects
// the per-message continuation link the service sends.
//
// This is a second pagination nested inside the first. The collection's own
// @odata.nextLink pages root messages and says nothing about replies, so a
// client that follows only the outer link -- which is every client written
// against every other Graph collection -- truncates each long thread and
// displays a conversation that stops mid-argument, with no error and no gap.
func PageExpandedReplies(limit int) Shaper {
	return func(r *Request, response *Response) {
		if r == nil {
			return
		}
		rewriteBody(response, func(node any) {
			object, ok := node.(map[string]any)
			if !ok {
				return
			}
			replies, isSlice := object["replies"].([]any)
			if !isSlice || len(replies) <= limit {
				return
			}
			object["replies"] = replies[:limit]
			object["replies@odata.nextLink"] = repliesLink(r.Path, object)
		})
	}
}

// RootChannelMessagesOnly drops replies from a channel message collection.
//
// A channel listing returns root messages and nothing else, however deep the
// paging goes. A transcript built from it alone shows every conversation
// opener and none of the conversation, so a fake that mixes replies into the
// collection hides the need to expand them or fetch them per message.
func RootChannelMessagesOnly() Shaper {
	return func(r *Request, response *Response) {
		if r == nil || !isChannelMessagePath(r.Path) || strings.Contains(r.Path, "/replies") {
			return
		}
		rewriteBody(response, func(node any) {
			object, ok := node.(map[string]any)
			if !ok {
				return
			}
			items, isSlice := object["value"].([]any)
			if !isSlice {
				return
			}
			roots := make([]any, 0, len(items))
			for _, item := range items {
				message, isObject := item.(map[string]any)
				if isObject {
					if parent, isString := message["replyToId"].(string); isString && parent != "" {
						continue
					}
				}
				roots = append(roots, item)
			}
			object["value"] = roots
		})
	}
}

// joinedTeamsPopulated are the only team properties GET /me/joinedTeams fills
// in. Every other property of the team is returned as null.
var joinedTeamsPopulated = map[string]bool{
	"id":          true,
	"displayName": true,
	"description": true,
	"isArchived":  true,
	"tenantId":    true,
	"@odata.type": true,
}

// PopulateOnlyJoinedTeamsFields nulls the team properties that listing joined
// teams does not fill in.
//
// The response carries the whole team shape, so it looks complete. It is not:
// webUrl, visibility, internalId and the settings all come back null, and only
// a Get on the individual team populates them. A client reading visibility off
// a listing sees an empty string and concludes the team is public, which is a
// different claim from "not loaded".
func PopulateOnlyJoinedTeamsFields() Shaper {
	return func(r *Request, response *Response) {
		if r == nil || !strings.Contains(strings.ToLower(r.Path), "/joinedteams") {
			return
		}
		rewriteBody(response, func(node any) {
			object, ok := node.(map[string]any)
			if !ok {
				return
			}
			// Only team objects, not the collection envelope around them.
			if _, hasName := object["displayName"]; !hasName {
				return
			}
			for key := range object {
				if !joinedTeamsPopulated[key] {
					object[key] = nil
				}
			}
		})
	}
}

// repliesLink builds the per-message continuation link for truncated replies.
//
// It is relative, and that is deliberate. The service sends an absolute link,
// but an absolute graph.microsoft.com link is one a test can never follow: the
// client refuses a host other than its base, so a fake emitting one makes the
// exact behavior this shaper exists to exercise undereferenceable, and the
// nested paging goes untested. A relative link resolves against whatever base
// the client has -- the fake under test, the real service in production -- so
// the same code path is exercised in both.
func repliesLink(requestPath string, message map[string]any) string {
	id, _ := message["id"].(string)
	return strings.TrimRight(requestPath, "/") + "/" + id + "/replies?$skiptoken=graphmock"
}

// rewriteBody decodes a response body, applies visit to every object in it,
// and writes it back. A body that is absent or is not JSON is left alone, so a
// shaper composes safely with a handler returning bytes or an error payload.
func rewriteBody(response *Response, visit func(any)) {
	if response == nil || len(response.body) == 0 {
		return
	}
	var payload any
	if err := json.Unmarshal(response.body, &payload); err != nil {
		return
	}
	walkJSON(payload, visit)
	if rewritten, err := json.Marshal(payload); err == nil {
		response.body = rewritten
	}
}

// walkJSON applies visit to every object and array element in a decoded body.
func walkJSON(node any, visit func(any)) {
	visit(node)
	switch value := node.(type) {
	case map[string]any:
		for _, child := range value {
			walkJSON(child, visit)
		}
	case []any:
		for _, child := range value {
			walkJSON(child, visit)
		}
	}
}

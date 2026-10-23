package graphmock_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	msgraph "github.com/albertocavalcante/msgraph-go"
	"github.com/albertocavalcante/msgraph-go/graphmock"
)

// chatMessage is the slice of the Teams message shape these tests assert on.
// It is a struct rather than a map so that a field renamed in the fake stops
// compiling instead of silently reading as absent.
type chatMessage struct {
	ID          string         `json:"id"`
	MessageType string         `json:"messageType"`
	ReplyToID   string         `json:"replyToId,omitempty"`
	From        map[string]any `json:"from"`
	EventDetail map[string]any `json:"eventDetail,omitempty"`
	Replies     []chatMessage  `json:"replies,omitempty"`
	RepliesNext string         `json:"replies@odata.nextLink,omitempty"`
}

type chatResource struct {
	ID       string           `json:"id"`
	Topic    *string          `json:"topic"`
	ChatType string           `json:"chatType"`
	Members  []map[string]any `json:"members,omitempty"`
	// MembersNext must stay absent. It is declared so a fake that starts
	// sending one fails the assertion below rather than being ignored.
	MembersNext string `json:"members@odata.nextLink,omitempty"`
}

func teamsServer(t *testing.T) *graphmock.Server {
	t.Helper()
	return graphmock.New(t,
		graphmock.WithConstraints(graphmock.Teams()...),
		graphmock.WithShapers(graphmock.TeamsShapers()...),
	)
}

// TestPreferHeaderDecidesWhetherSystemEventsAreNamed is the negative control
// for the whole bundle.
//
// Both halves have to hold. Without the Prefer directive the system event must
// come back as unknownFutureValue, and with it the real name must survive. A
// shaper that never fired would pass the second half alone, so asserting only
// the happy path would leave the check blind to itself.
func TestPreferHeaderDecidesWhetherSystemEventsAreNamed(t *testing.T) {
	t.Parallel()

	server := teamsServer(t)
	server.Handle("GET /chats/{chat}/messages", func(_ *graphmock.Request) graphmock.Response {
		return graphmock.Collection([]chatMessage{
			{ID: "1", MessageType: "message", From: map[string]any{"user": map[string]any{"id": "u"}}},
			{ID: "2", MessageType: "systemEventMessage", EventDetail: map[string]any{
				"@odata.type": "#microsoft.graph.chatRenamedEventMessageDetail",
			}},
		}, "")
	})

	const path = "/chats/19:abc@thread.v2/messages"
	client := server.Client()
	ctx := context.Background()

	blind, _, err := msgraph.Get[msgraph.Page[chatMessage]](ctx, client, path, msgraph.Params{})
	if err != nil {
		t.Fatalf("without Prefer: %v", err)
	}
	if got := blind.Value[1].MessageType; got != "unknownFutureValue" {
		t.Fatalf("without Prefer, messageType = %q, want unknownFutureValue -- the shaper did not fire, so this suite cannot see the bug it exists for", got)
	}

	named, _, err := msgraph.Do[msgraph.Page[chatMessage]](ctx, client, msgraph.Request{
		Method: "GET",
		URL:    path,
		Prefer: []msgraph.PreferDirective{msgraph.PreferIncludeUnknownEnumMembers},
	})
	if err != nil {
		t.Fatalf("with Prefer: %v", err)
	}
	if got := named.Value[1].MessageType; got != "systemEventMessage" {
		t.Fatalf("with Prefer, messageType = %q, want systemEventMessage", got)
	}
}

// TestSystemEventMessageHasNoSender pins that the sender really is absent, so
// a client cannot pass here by dereferencing it unconditionally.
func TestSystemEventMessageHasNoSender(t *testing.T) {
	t.Parallel()

	server := teamsServer(t)
	server.Handle("GET /chats/{chat}/messages", func(_ *graphmock.Request) graphmock.Response {
		return graphmock.Collection([]chatMessage{
			// A sender the fake supplies on purpose: the shaper has to remove
			// it, or it is not reproducing the service.
			{ID: "2", MessageType: "systemEventMessage",
				From:        map[string]any{"user": map[string]any{"id": "u"}},
				EventDetail: map[string]any{"@odata.type": "#microsoft.graph.membersAddedEventMessageDetail"}},
		}, "")
	})

	page, _, err := msgraph.Get[msgraph.Page[chatMessage]](context.Background(), server.Client(),
		"/chats/19:abc@thread.v2/messages", msgraph.Params{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Value[0].From != nil {
		t.Fatalf("from = %v, want nil on a system event message", page.Value[0].From)
	}
}

// TestMismatchedFilterIsDroppedNotRefused pins the most dangerous behavior in
// the Teams API: the filter is discarded and the whole collection is returned,
// successfully.
func TestMismatchedFilterIsDroppedNotRefused(t *testing.T) {
	t.Parallel()

	var sawFilter string
	server := teamsServer(t)
	server.Handle("GET /chats/{chat}/messages", func(r *graphmock.Request) graphmock.Response {
		sawFilter = r.Query.Filter
		return graphmock.Collection([]chatMessage{{ID: "1", MessageType: "message"}}, "")
	})

	ctx := context.Background()
	client := server.Client()
	const path = "/chats/19:abc@thread.v2/messages"

	// Filtering on createdDateTime while ordering by lastModifiedDateTime.
	_, _, err := msgraph.Get[msgraph.Page[chatMessage]](ctx, client, path, msgraph.Params{
		Where: msgraph.Ge("createdDateTime", "2026-01-01T00:00:00Z"),
		Sort:  []msgraph.Order{msgraph.Desc("lastModifiedDateTime")},
	})
	if err != nil {
		t.Fatalf("a mismatched filter must be ignored, not refused: %v", err)
	}
	if sawFilter != "" {
		t.Fatalf("handler saw filter %q, want it dropped", sawFilter)
	}

	// Matching the two makes the filter survive.
	_, _, err = msgraph.Get[msgraph.Page[chatMessage]](ctx, client, path, msgraph.Params{
		Where: msgraph.Ge("createdDateTime", "2026-01-01T00:00:00Z"),
		Sort:  []msgraph.Order{msgraph.Desc("createdDateTime")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sawFilter == "" {
		t.Fatal("a filter matching the sort property was dropped, so this constraint drops everything and proves nothing")
	}
}

// TestAscendingMessageSortIsRefused pins the one message-ordering mistake the
// service does report.
func TestAscendingMessageSortIsRefused(t *testing.T) {
	t.Parallel()

	server := teamsServer(t)
	server.Handle("GET /chats/{chat}/messages", func(_ *graphmock.Request) graphmock.Response {
		return graphmock.Collection([]chatMessage{}, "")
	})

	_, _, err := msgraph.Get[msgraph.Page[chatMessage]](context.Background(), server.Client(),
		"/chats/19:abc@thread.v2/messages", msgraph.Params{
			Sort: []msgraph.Order{msgraph.Asc("createdDateTime")},
		})
	if err == nil {
		t.Fatal("ascending order must be refused")
	}
	if !msgraph.IsStatus(err, 400) {
		t.Fatalf("err = %v, want 400", err)
	}
}

// TestSelectOnMessagesIsRefused pins that $select is refused rather than
// ignored, which is the opposite of how the filter behaves on the same path.
func TestSelectOnMessagesIsRefused(t *testing.T) {
	t.Parallel()

	server := teamsServer(t)
	server.Handle("GET /chats/{chat}/messages", func(_ *graphmock.Request) graphmock.Response {
		return graphmock.Collection([]chatMessage{}, "")
	})

	_, _, err := msgraph.Get[msgraph.Page[chatMessage]](context.Background(), server.Client(),
		"/chats/19:abc@thread.v2/messages", msgraph.Params{Select: []string{"id", "body"}})
	if err == nil || !msgraph.IsStatus(err, 400) {
		t.Fatalf("err = %v, want a 400 refusing $select", err)
	}
}

// TestOversizedPageIsClampedSilently pins that asking for more than the cap is
// answered with fewer items and no error, so a short page cannot be read as a
// complete one.
func TestOversizedPageIsClampedSilently(t *testing.T) {
	t.Parallel()

	var servedTop int
	server := teamsServer(t)
	server.Handle("GET /chats", func(r *graphmock.Request) graphmock.Response {
		servedTop = r.Query.Top
		return graphmock.Collection([]chatResource{}, "")
	})

	_, _, err := msgraph.Get[msgraph.Page[chatResource]](context.Background(), server.Client(),
		"/chats", msgraph.Params{Top: 200})
	if err != nil {
		t.Fatalf("an oversized $top must be clamped, not refused: %v", err)
	}
	if servedTop != graphmock.TeamsChatPageCap {
		t.Fatalf("served $top = %d, want %d", servedTop, graphmock.TeamsChatPageCap)
	}
}

// TestOneOnOneChatHasNoTopic pins the absent name that makes a chat list
// render as blank rows.
func TestOneOnOneChatHasNoTopic(t *testing.T) {
	t.Parallel()

	supplied := "a topic the service would not send"
	server := teamsServer(t)
	server.Handle("GET /chats", func(_ *graphmock.Request) graphmock.Response {
		return graphmock.Collection([]chatResource{
			{ID: "19:a@unq.gbl.spaces", ChatType: "oneOnOne", Topic: &supplied},
			{ID: "19:b@thread.v2", ChatType: "group", Topic: &supplied},
		}, "")
	})

	page, _, err := msgraph.Get[msgraph.Page[chatResource]](context.Background(), server.Client(),
		"/chats", msgraph.Params{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Value[0].Topic != nil {
		t.Fatalf("oneOnOne topic = %q, want null", *page.Value[0].Topic)
	}
	if page.Value[1].Topic == nil {
		t.Fatal("a group chat keeps its topic; stripping both means this shaper is indiscriminate")
	}
}

// TestExpandedMembersTruncateWithoutALink pins the truncation and, more
// importantly, the missing continuation link: a client that follows links
// everywhere else finds nothing to follow here.
func TestExpandedMembersTruncateWithoutALink(t *testing.T) {
	t.Parallel()

	members := make([]map[string]any, 60)
	for i := range members {
		members[i] = map[string]any{"id": "member"}
	}

	server := teamsServer(t)
	server.Handle("GET /chats", func(_ *graphmock.Request) graphmock.Response {
		return graphmock.Collection([]chatResource{
			{ID: "19:b@thread.v2", ChatType: "group", Members: members},
		}, "")
	})

	page, _, err := msgraph.Get[msgraph.Page[chatResource]](context.Background(), server.Client(),
		"/chats", msgraph.Params{Expand: []string{"members"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(page.Value[0].Members); got != graphmock.TeamsExpandedMemberCap {
		t.Fatalf("members = %d, want %d", got, graphmock.TeamsExpandedMemberCap)
	}
	if page.Value[0].MembersNext != "" {
		t.Fatalf("members@odata.nextLink = %q, want absent -- inventing one hides the whole hazard",
			page.Value[0].MembersNext)
	}
}

// TestChannelListingReturnsRootsAndPagesRepliesSeparately pins the two channel
// behaviors that silently truncate a transcript.
func TestChannelListingReturnsRootsAndPagesRepliesSeparately(t *testing.T) {
	t.Parallel()

	replies := make([]chatMessage, graphmock.TeamsExpandedReplyCap+5)
	for i := range replies {
		replies[i] = chatMessage{ID: "r", MessageType: "message", ReplyToID: "1"}
	}

	server := teamsServer(t)
	server.Handle("GET /teams/{team}/channels/{channel}/messages", func(_ *graphmock.Request) graphmock.Response {
		return graphmock.Collection([]chatMessage{
			{ID: "1", MessageType: "message", Replies: replies},
			// A reply the fake mixes into the collection on purpose. The
			// service never does, so the shaper must drop it.
			{ID: "2", MessageType: "message", ReplyToID: "1"},
		}, "")
	})

	page, _, err := msgraph.Get[msgraph.Page[chatMessage]](context.Background(), server.Client(),
		"/teams/t/channels/19:c@thread.tacv2/messages", msgraph.Params{Expand: []string{"replies"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Value) != 1 {
		t.Fatalf("collection returned %d messages, want only the root", len(page.Value))
	}
	if got := len(page.Value[0].Replies); got != graphmock.TeamsExpandedReplyCap {
		t.Fatalf("replies = %d, want %d", got, graphmock.TeamsExpandedReplyCap)
	}
	if page.Value[0].RepliesNext == "" {
		t.Fatal("a truncated thread must carry replies@odata.nextLink, or nothing tells the client it stopped early")
	}
	if page.NextLink == page.Value[0].RepliesNext {
		t.Fatal("the reply link must be distinct from the collection's own link")
	}
}

func TestChannelMessageQueryRejectsChatOnlyOptions(t *testing.T) {
	t.Parallel()
	server := teamsServer(t)
	server.Handle("GET /teams/{team}/channels/{channel}/messages", func(_ *graphmock.Request) graphmock.Response {
		return graphmock.Collection([]chatMessage{}, "")
	})
	path := "/teams/t/channels/c/messages"
	for _, params := range []msgraph.Params{
		{Sort: []msgraph.Order{msgraph.Desc("createdDateTime")}},
		{Where: msgraph.Lt("createdDateTime", time.Now())},
		{Expand: []string{"members"}},
	} {
		_, _, err := msgraph.Get[msgraph.Page[chatMessage]](context.Background(), server.Client(), path, params)
		if !msgraph.IsStatus(err, http.StatusBadRequest) {
			t.Errorf("channel query %+v: error = %v, want 400", params, err)
		}
	}
}

func TestChannelRepliesQueryRejectsChatOnlyOptions(t *testing.T) {
	t.Parallel()
	server := teamsServer(t)
	server.Handle("GET /teams/{team}/channels/{channel}/messages/{message}/replies", func(_ *graphmock.Request) graphmock.Response {
		return graphmock.Collection([]chatMessage{}, "")
	})
	path := "/teams/t/channels/c/messages/m/replies"
	_, _, err := msgraph.Get[msgraph.Page[chatMessage]](context.Background(), server.Client(), path,
		msgraph.Params{Sort: []msgraph.Order{msgraph.Desc("createdDateTime")}})
	if !msgraph.IsStatus(err, http.StatusBadRequest) {
		t.Errorf("replies query: error = %v, want 400", err)
	}
	_, _, err = msgraph.Get[msgraph.Page[chatMessage]](context.Background(), server.Client(), path,
		msgraph.Params{Expand: []string{"replies"}})
	if !msgraph.IsStatus(err, http.StatusBadRequest) {
		t.Errorf("expanded replies query: error = %v, want 400", err)
	}
}

func TestExpandedRepliesDefaultLimitIsTwoHundred(t *testing.T) {
	t.Parallel()
	if graphmock.TeamsExpandedReplyCap != 200 {
		t.Fatalf("expanded reply limit = %d, want 200", graphmock.TeamsExpandedReplyCap)
	}
}

// TestJoinedTeamsPopulatesOnlyTheDocumentedFields pins that the listing's
// extra properties are null rather than meaningful.
func TestJoinedTeamsPopulatesOnlyTheDocumentedFields(t *testing.T) {
	t.Parallel()

	type team struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayName"`
		Visibility  string `json:"visibility"`
		WebURL      string `json:"webUrl"`
	}

	server := teamsServer(t)
	server.Handle("GET /me/joinedTeams", func(_ *graphmock.Request) graphmock.Response {
		return graphmock.Collection([]team{
			{ID: "t", DisplayName: "Team", Visibility: "public", WebURL: "https://teams.example"},
		}, "")
	})

	page, _, err := msgraph.Get[msgraph.Page[team]](context.Background(), server.Client(),
		"/me/joinedTeams", msgraph.Params{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Value[0].DisplayName != "Team" {
		t.Fatalf("displayName = %q, want it populated", page.Value[0].DisplayName)
	}
	if page.Value[0].Visibility != "" || page.Value[0].WebURL != "" {
		t.Fatalf("visibility = %q, webUrl = %q; both must be null from a joinedTeams listing",
			page.Value[0].Visibility, page.Value[0].WebURL)
	}
}

// TestSearchRefusesASortAndRequiresAnEntityType pins the two ways a message
// search is built wrong.
func TestSearchRefusesASortAndRequiresAnEntityType(t *testing.T) {
	t.Parallel()

	server := teamsServer(t)
	server.Handle("POST /search/query", func(_ *graphmock.Request) graphmock.Response {
		return graphmock.Item(map[string]any{"value": []any{}})
	})

	ctx := context.Background()
	client := server.Client()

	sorted := map[string]any{"requests": []any{map[string]any{
		"entityTypes":    []any{"chatMessage"},
		"query":          map[string]any{"queryString": "budget"},
		"sortProperties": []any{map[string]any{"name": "createdDateTime"}},
	}}}
	if _, err := msgraph.PostNoContent(ctx, client, "/search/query", msgraph.Params{}, sorted); err == nil {
		t.Fatal("a sorted chatMessage search must be refused")
	}

	untyped := map[string]any{"requests": []any{map[string]any{
		"query": map[string]any{"queryString": "budget"},
	}}}
	if _, err := msgraph.PostNoContent(ctx, client, "/search/query", msgraph.Params{}, untyped); err == nil {
		t.Fatal("a search naming no entity type must be refused, not answered with an empty result")
	}

	valid := map[string]any{"requests": []any{map[string]any{
		"entityTypes": []any{"chatMessage"},
		"query":       map[string]any{"queryString": "budget"},
	}}}
	if _, err := msgraph.PostNoContent(ctx, client, "/search/query", msgraph.Params{}, valid); err != nil {
		t.Fatalf("a well-formed search was refused: %v", err)
	}
}

// TestTeamsConstraintsLeaveOtherWorkloadsAlone pins that these are scoped to
// Teams paths. They live in the same package as the Exchange bundle, and a
// path match that is too loose would refuse mail requests -- the mirror of the
// bug that motivated the Teams routes in the first place.
func TestTeamsConstraintsLeaveOtherWorkloadsAlone(t *testing.T) {
	t.Parallel()

	var sawFilter string
	server := teamsServer(t)
	server.Handle("GET /me/messages", func(r *graphmock.Request) graphmock.Response {
		sawFilter = r.Query.Filter
		return graphmock.Collection([]chatMessage{}, "")
	})

	_, _, err := msgraph.Get[msgraph.Page[chatMessage]](context.Background(), server.Client(),
		"/me/messages", msgraph.Params{
			Select: []string{"id"},
			Where:  msgraph.Eq("isRead", false),
			Sort:   []msgraph.Order{msgraph.Desc("receivedDateTime")},
		})
	if err != nil {
		t.Fatalf("a mail request must pass through the Teams constraints untouched: %v", err)
	}
	if !strings.Contains(sawFilter, "isRead") {
		t.Fatalf("mail filter = %q, want it preserved", sawFilter)
	}
}

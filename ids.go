package msgraph

import (
	"context"
	"net/url"
)

// ExchangeIDFormat is a Microsoft Graph exchangeIdFormat value.
type ExchangeIDFormat string

const (
	ExchangeIDFormatEntryID              ExchangeIDFormat = "entryId"
	ExchangeIDFormatEWSID                ExchangeIDFormat = "ewsId"
	ExchangeIDFormatImmutableEntryID     ExchangeIDFormat = "immutableEntryId"
	ExchangeIDFormatRESTID               ExchangeIDFormat = "restId"
	ExchangeIDFormatRESTImmutableEntryID ExchangeIDFormat = "restImmutableEntryId"
)

// TranslateExchangeIDsRequest is the body accepted by
// /me/translateExchangeIds and /users/{id}/translateExchangeIds.
type TranslateExchangeIDsRequest struct {
	InputIDs     []string         `json:"inputIds"`
	SourceIDType ExchangeIDFormat `json:"sourceIdType"`
	TargetIDType ExchangeIDFormat `json:"targetIdType"`
}

// TranslateExchangeIDsResponse is the response from translateExchangeIds.
type TranslateExchangeIDsResponse struct {
	Value []TranslatedExchangeID `json:"value"`
}

// TranslatedExchangeID is one translated Exchange identifier.
type TranslatedExchangeID struct {
	SourceID string `json:"sourceId"`
	TargetID string `json:"targetId"`
}

// TranslateExchangeIDs converts Exchange-family identifiers for the signed-in
// user ("", "me") or the supplied user ID / UPN.
func (c *Client) TranslateExchangeIDs(
	ctx context.Context,
	user string,
	inputIDs []string,
	sourceIDType ExchangeIDFormat,
	targetIDType ExchangeIDFormat,
) ([]TranslatedExchangeID, *Response, error) {
	path := translateExchangeIDsPath(user)
	var out TranslateExchangeIDsResponse
	resp, err := c.Post(ctx, path, Params{}, TranslateExchangeIDsRequest{
		InputIDs:     inputIDs,
		SourceIDType: sourceIDType,
		TargetIDType: targetIDType,
	}, &out)
	if err != nil {
		return nil, resp, err
	}
	return out.Value, resp, nil
}

func translateExchangeIDsPath(user string) string {
	switch user {
	case "", "me":
		return "/me/translateExchangeIds"
	default:
		return "/users/" + url.PathEscape(user) + "/translateExchangeIds"
	}
}

package graphmock

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// WindowsTimeZones are the names Graph uses when a request asks for a zone.
// None is an IANA name, so none resolves through Go's time package: a client
// that assumes otherwise computes the wrong instant without an error anywhere.
var WindowsTimeZones = []string{
	"Pacific Standard Time",
	"Eastern Standard Time",
	"GMT Standard Time",
	"W. Europe Standard Time",
	"Tokyo Standard Time",
	"E. South America Standard Time",
}

// NameZonesTheWindowsWay rewrites every timeZone in a response to a Windows
// identifier, the way Graph does once a request asks for a zone.
//
// This is a shaper rather than a constraint because nothing is refused. The
// response is perfectly valid and the client still gets it wrong, which is the
// harder failure to find: a calendar that renders confidently and is hours or
// a day out. Rendering an all-day event this way showed every holiday on the
// wrong date.
func NameZonesTheWindowsWay(zone string) Shaper {
	if zone == "" {
		zone = WindowsTimeZones[0]
	}
	return func(_ *Request, response *Response) {
		if response == nil || len(response.body) == 0 {
			return
		}
		if !strings.Contains(string(response.body), `"timeZone"`) {
			return
		}

		var payload any
		if err := json.Unmarshal(response.body, &payload); err != nil {
			return
		}
		rewriteZones(payload, zone)
		if rewritten, err := json.Marshal(payload); err == nil {
			response.body = rewritten
		}
	}
}

// rewriteZones walks a decoded body replacing every timeZone value.
func rewriteZones(node any, zone string) {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "timeZone" {
				if _, isString := child.(string); isString {
					value[key] = zone
					continue
				}
			}
			rewriteZones(child, zone)
		}
	case []any:
		for _, child := range value {
			rewriteZones(child, zone)
		}
	}
}

// StripHeader removes a response header the real service does not send.
//
// Mail is the case worth reproducing: there is no ETag header on a message,
// the version is the @odata.etag body property, and a client reading the header
// finds nothing and silently turns every conditional write into an
// unconditional one.
func StripHeader(name string) Shaper {
	return func(_ *Request, response *Response) {
		if response == nil || response.Header == nil {
			return
		}
		// Header.Del only matches the canonical spelling, and a handler that
		// built its header as a map literal has whatever it typed. Removing
		// only the canonical form would silently leave the header in place.
		response.Header.Del(name)
		canonical := http.CanonicalHeaderKey(name)
		for key := range response.Header {
			if http.CanonicalHeaderKey(key) == canonical {
				delete(response.Header, key)
			}
		}
	}
}

// OutlookChunkAccepted is what an Outlook upload session answers for a chunk
// that is not the last one.
//
// It is 200 with the ranges it still wants, not the 202 an upload session
// suggests. Reading the status as completion truncates the attachment at the
// first chunk, and a fake that answered 202 encoded that mistake so well the
// suite stayed green while real uploads lost data. The body decides, not the
// status.
func OutlookChunkAccepted(receivedSoFar int64) Response {
	return marshal(http.StatusOK, map[string]any{
		"expirationDateTime": "2026-12-31T23:59:59Z",
		"nextExpectedRanges": []string{strconv.FormatInt(receivedSoFar, 10) + "-"},
	})
}

// OutlookChunkComplete is what the session answers for the final chunk: 201
// with the attachment it created.
func OutlookChunkComplete[T any](attachment T) Response {
	return marshal(http.StatusCreated, attachment)
}

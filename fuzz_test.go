package msgraph

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// parseODataString is a strict reader for an OData string literal: it is the
// inverse of quoteODataString. The fuzz property below is that the two
// round-trip for every input, which is what "the value cannot escape the
// literal" actually means.
func parseODataString(literal string) (string, error) {
	if len(literal) < 2 || literal[0] != '\'' || literal[len(literal)-1] != '\'' {
		return "", errors.New("not a quoted literal")
	}
	body := literal[1 : len(literal)-1]

	var out strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] != '\'' {
			out.WriteByte(body[i])
			continue
		}
		// A quote inside the body is only legal as a doubled pair. A lone
		// quote means the literal terminated early, which is exactly the
		// injection this encoding exists to prevent.
		if i+1 >= len(body) || body[i+1] != '\'' {
			return "", errors.New("unescaped quote inside literal")
		}
		out.WriteByte('\'')
		i++
	}
	return out.String(), nil
}

func FuzzODataStringLiteral(f *testing.F) {
	for _, seed := range []string{
		"", "plain", "O'Brien", "''", "'", "'''",
		`x' or isRead eq true or subject eq '`,
		"a'b'c", "\x00", "🙂", "line\nbreak", strings.Repeat("'", 33),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		literal := quoteODataString(value)

		decoded, err := parseODataString(literal)
		if err != nil {
			t.Fatalf("quoteODataString(%q) = %q, which does not parse as one literal: %v", value, literal, err)
		}
		if decoded != value {
			t.Fatalf("round trip changed the value: %q -> %q -> %q", value, literal, decoded)
		}
	})
}

// The same property, one level up: no filter value can inject OData syntax
// into the rendered $filter, even after query encoding.
func FuzzFilterValueCannotEscape(f *testing.F) {
	for _, seed := range []string{"", "a", "O'Brien", "' or 1 eq 1 or '", "a,b", "%27"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		rendered := Eq("subject", value).ODataFilter()

		const prefix = "subject eq "
		if !strings.HasPrefix(rendered, prefix) {
			t.Fatalf("filter = %q, want prefix %q", rendered, prefix)
		}
		decoded, err := parseODataString(strings.TrimPrefix(rendered, prefix))
		if err != nil {
			t.Fatalf("filter %q does not end in a single well-formed literal: %v", rendered, err)
		}
		if decoded != value {
			t.Fatalf("value changed: %q -> %q", value, decoded)
		}

		// And it must survive URL encoding unchanged.
		encoded := Params{Where: Eq("subject", value)}.Values().Encode()
		parsed, err := url.ParseQuery(encoded)
		if err != nil {
			t.Fatalf("encoded params do not parse: %v", err)
		}
		if got := parsed.Get("$filter"); got != rendered {
			t.Fatalf("query encoding changed the filter: %q -> %q", rendered, got)
		}
	})
}

// The bearer token must never be sent to a host other than the base host.
// resolveURL is the only thing standing between a hostile link and the token,
// so it gets arbitrary input.
func FuzzResolveURLNeverLeavesBaseHost(f *testing.F) {
	for _, seed := range []string{
		"/me/messages",
		"https://graph.microsoft.com/v1.0/me/messages",
		"https://evil.example/steal",
		"//evil.example/steal",
		"http://graph.microsoft.com/v1.0",
		"https://graph.microsoft.com@evil.example/x",
		"https://graph.microsoft.com:443/v1.0/me",
		"", ":", "///", "\\\\evil.example\\x", "https://GRAPH.microsoft.com/v1.0/me",
	} {
		f.Add(seed)
	}

	client, err := New(staticToken("t"), WithBaseURL("https://graph.microsoft.com/v1.0"))
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		resolved, err := client.resolveURL(raw)
		if err != nil {
			return // Refusing is always an acceptable outcome.
		}
		if !strings.EqualFold(resolved.Host, "graph.microsoft.com") {
			t.Fatalf("resolveURL(%q) = %q, which would send the token to %q", raw, resolved, resolved.Host)
		}
		if !strings.EqualFold(resolved.Scheme, "https") {
			t.Fatalf("resolveURL(%q) = %q, downgraded to scheme %q", raw, resolved, resolved.Scheme)
		}
	})
}

// A malformed or hostile error body must not panic the client, and must not
// invent a request ID that was never sent.
func FuzzParseAPIError(f *testing.F) {
	for _, seed := range []string{
		"", "{}", `{"error":{}}`,
		`{"error":{"code":"x","message":"y","target":"z","details":[{"code":"d"}]}}`,
		`{"error":{"innerError":{"innerError":{"innerError":{"request-id":"deep"}}}}}`,
		`{"error":`, "null", "[]", "\x00\x01",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, body string) {
		apiErr := parseAPIError(http.StatusBadRequest, http.Header{}, []byte(body))
		if apiErr == nil {
			t.Fatal("parseAPIError returned nil")
		}
		if apiErr.StatusCode != http.StatusBadRequest {
			t.Fatalf("StatusCode = %d", apiErr.StatusCode)
		}
		if apiErr.RetryAfter < 0 {
			t.Fatalf("RetryAfter = %v, want non-negative", apiErr.RetryAfter)
		}
		// Error() must not panic and must mention the status.
		if msg := apiErr.Error(); !strings.Contains(msg, "400") {
			t.Fatalf("Error() = %q, want it to mention the status", msg)
		}
		if !errors.Is(apiErr, ErrRequestFailed) {
			t.Fatal("APIError no longer wraps ErrRequestFailed")
		}
	})
}

// A hostile Retry-After must never produce a zero or negative sleep, which
// would turn backoff into a busy loop against the service.
func FuzzRetryDelayStaysPositive(f *testing.F) {
	for _, seed := range []string{"", "0", "-1", "1", "99999999999999999999", "not-a-date", "Wed, 21 Oct 2015 07:28:00 GMT"} {
		f.Add(seed, 0)
	}

	f.Fuzz(func(t *testing.T, retryAfter string, attempt int) {
		header := http.Header{}
		if retryAfter != "" {
			header.Set("Retry-After", retryAfter)
		}
		delay := retryDelay(header, attempt, 30*time.Second)
		if delay <= 0 {
			t.Fatalf("retryDelay(%q, attempt=%d) = %v, want positive", retryAfter, attempt, delay)
		}
		if delay > 30*time.Second {
			t.Fatalf("retryDelay(%q, attempt=%d) = %v, want it capped", retryAfter, attempt, delay)
		}
	})
}

// Params must always render to something that parses back as a query string.
func FuzzParamsValues(f *testing.F) {
	f.Add("subject", "value", 10, 0, true)
	f.Add("", "", -1, -1, false)

	f.Fuzz(func(t *testing.T, field, value string, top, skip int, count bool) {
		params := Params{
			Select: []string{field},
			Where:  Contains(field, value),
			Sort:   []Order{Desc(field)},
			Top:    top,
			Skip:   skip,
			Count:  count,
		}
		if err := params.Validate(); err != nil {
			// Negative paging is rejected rather than silently dropped.
			if top >= 0 && skip >= 0 {
				t.Fatalf("Validate() = %v for non-negative paging", err)
			}
			return
		}
		if _, err := url.ParseQuery(params.Values().Encode()); err != nil {
			t.Fatalf("params did not encode to a parseable query: %v", err)
		}
	})
}

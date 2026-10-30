# msgraph-go

`msgraph-go` is a small, dependency-free Microsoft Graph REST client for Go.
It owns transport concerns that every typed Microsoft SDK in this workspace
needs: bearer-token injection, OData query construction, JSON request/response
handling, paging, delta sync, throttling retries, and structured Graph errors.

It deliberately does not import `msauth-go`. Callers provide a `TokenSource`,
so CLIs can wire Microsoft delegated auth while tests and services can provide
their own token source.

## Usage

```go
client, err := msgraph.New(msgraph.TokenSourceFunc(func(ctx context.Context) (string, error) {
    return "access-token", nil
}))
```

With `msauth-go`, whose `TokenSource` satisfies the interface structurally:

```go
client, err := msgraph.New(auth.TokenSource("Mail.Read"))
```

### Typed requests

The generic helpers name the result type once and infer it everywhere after,
instead of routing it through an `any` out-parameter:

```go
message, resp, err := msgraph.Get[Message](ctx, client, "/me/messages/"+id, msgraph.Params{})

items, err := msgraph.Collect[Message](ctx, client, "/me/messages", msgraph.Params{Top: 50})

for message, err := range msgraph.Items[Message](ctx, client, "/me/messages", msgraph.Params{}) {
    // ...
}
```

`Get`, `Post`, `Patch`, `Do`, `Collect`, and `First` are typed; `PostNoContent`
and `Delete` discard the body. The `Client` methods remain available for
decoding into an existing value, streaming into an `io.Writer`, or ignoring the
response body.

### Typed filters

`Params.Where` builds `$filter` from a typed expression tree, which keeps
literals escaped. A value containing an apostrophe — `O'Brien` — would
otherwise close the OData string literal early and change what the query means:

```go
params := msgraph.Params{
    Where: msgraph.And(
        msgraph.Eq("isRead", false),
        msgraph.Ge("receivedDateTime", cutoff),          // time.Time, normalized to UTC
        msgraph.Contains("subject", userSuppliedText),   // escaped
    ),
    Sort:   []msgraph.Order{msgraph.Desc("receivedDateTime")},
    Select: []string{"id", "subject", "from"},
    Top:    25,
}
```

Available: `Eq`, `Ne`, `Gt`, `Ge`, `Lt`, `Le`, `In`, `IsNull`, `IsNotNull`,
`Contains`, `StartsWith`, `EndsWith`, `And`, `Or`, `Not`, `Any`, `All`,
`Field`, and `Raw` as the escape hatch. The `Literal` constraint admits
strings, bools, the integer and float types, and `time.Time`; anything else is
a compile error. `Params.Filter` and `Params.OrderBy` still accept raw strings
and are combined with the typed forms.

### Transport behavior

- **Absolute request URLs must be same-origin with the base URL.** Every
  request carries a bearer token, so an off-origin URL — a spoofed
  `@odata.nextLink`, a caller-supplied link — would hand that token to a third
  party. Those requests fail with `ErrUntrustedHost`; `WithAllowedHosts` opts
  specific hosts in. Scheme downgrades are rejected on the same grounds.
- Destinations are validated before the token source is called. URL user info,
  fragments, malformed authorities and invalid base URLs are rejected. Origins
  compare scheme, case-insensitive hostname and effective numeric port; omitted
  HTTP/HTTPS ports are equivalent to explicit `80`/`443`.
- **Redirects must stay on the initial request's origin.** `WithAllowedHosts`
  permits direct requests only, and does not authorize a redirect to another
  allowed host. `WithHTTPClient` copies client configuration and wraps its
  redirect callback: the callback can further restrict behavior, but cannot
  change the destination or method to bypass these checks. The standard
  ten-request redirect limit applies when no callback is supplied; a supplied
  callback retains its own redirect-count policy and `http.ErrUseLastResponse`
  behavior.
- Redirects are refused for mutating methods, including method-preserving
  `307`/`308` responses and `301`/`302`/`303` responses that would rewrite a
  mutation to `GET`. The initial request may already have changed the server;
  a refused redirect does not prove that no side effect occurred. Redirect
  refusals wrap `ErrRedirectRefused` and are never retried, even when
  `WithRetryUnsafeMethods(true)` is set. Other retry rules are unchanged.
  A malformed `Location` can fail inside `net/http` before the redirect
  callback runs; that remains a transport error under the existing retry
  rules. Enabling unsafe retries therefore still permits replay after such
  an uncertain failure. Unsafe retries remain disabled by default.
- HTTP transport error strings omit request URLs, cursor queries and callback
  text. `errors.Is`/`errors.As` retain access to the original cause; explicitly
  inspecting or logging that cause may reveal its URL.
- Retries are enabled for safe methods (`GET`, `HEAD`, `OPTIONS`) on throttling
  and transient server errors. Mutating methods are not retried unless
  `WithRetryUnsafeMethods(true)` is set explicitly.
- Retry sleeps honor `Retry-After` and are capped by `WithMaxRetryDelay`
  (`30s` by default).
- Passing an `io.Writer` as `out` streams successful response bodies instead of
  buffering them first.
- `Params.Validate` rejects negative `Top`/`Skip` with `ErrInvalidParams`
  instead of silently dropping them; the client validates on every request.
- `Request.IfMatch` and `Request.IfNoneMatch` send conditional headers.
  `Response.ETag` carries the tag back, a matched conditional GET returns
  `ErrNotModified`, and a stale `If-Match` surfaces via `IsPreconditionFailed`.
- `APIError` carries the status, service `Code`, `Message`, `Target`,
  `Details`, `RequestID`, and the parsed `Retry-After`. Classify with
  `IsNotFound`, `IsUnauthorized`, `IsForbidden`, `IsThrottled`, `IsConflict`,
  `IsPreconditionFailed`, `IsStatus`, or `HasCode`.
- `BatchStrict` returns all batch responses plus a `BatchError` when any
  subrequest fails.
- `Pages`/`Items` detect repeated `nextLink` values, and `WithMaxPages` can cap
  traversal. `Page[T]` also exposes `@odata.count` and `@odata.deltaLink`.
- `Delta` drains a delta query and returns the changed items plus the
  `deltaLink` to persist as the sync position.
- `SuggestDelegatedScopes` provides conservative delegated-scope hints for
  common raw Graph routes.
- `TranslateExchangeIDs` wraps `/me/translateExchangeIds` and
  `/users/{id}/translateExchangeIds` for Exchange/REST/immutable ID conversion.

## Develop

```sh
just ci
```

Manual live smoke test:

```sh
cd cmd/msgraph-live-smoke
go run .
go run . -show-messages # opt in to printing sender/subject metadata
go run . # second run should reuse the msauth-go token cache silently
```

### Document downloads and conversion

`Download` is the explicit file-content path for Graph's preauthenticated
redirect protocol. It keeps ordinary `Do`/`Get` destination rules unchanged:

```go
_, err := client.Download(ctx, "/me/drive/items/ITEM_ID/content", msgraph.Params{
    Custom: url.Values{"format": {"pdf"}},
}, output)
```

The initial Graph GET may redirect once to an HTTPS content URL. The separate
content request carries no bearer token, cookies, or Referer; subsequent
redirects and unsolicited partial responses are refused. Downloads obey
`WithMaxResponseBytes`, preserve inspectable error causes without printing
signed URLs, and never retry a partially streamed response. Stage file output
and publish it only after success.

The production CLI is developed separately in `msgraph-cli`; applications can
use this SDK without depending on its authentication or command-line code.

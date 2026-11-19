# Observability

When a command built on this client is slow, the time can go to DNS and TLS
setup, token acquisition, the Graph service, retries, or throttling waits. The
client records each of these as OpenTelemetry spans, and can log its retry
decisions. Both are off by default and cost close to nothing when off.

## Turning tracing on

There is no option to set. The client takes its tracer from the span already
in the request context:

```go
ctx, span := tracerProvider.Tracer("my-cli").Start(ctx, "sync mailbox")
defer span.End()

messages, err := msgraph.Collect[Message](ctx, client, "/me/messages", msgraph.Params{})
```

With a context that carries no span, the OpenTelemetry API's built-in no-op
tracer is used, no `httptrace` hooks are installed, and response bodies are not
wrapped. The module depends only on the OpenTelemetry trace API, never on the
SDK or an exporter, and uses no global tracer provider. A context holding only
a remote span context, such as one extracted from incoming headers, carries no
tracer provider either: start a local span with your provider first.

## Span tree

```mermaid
flowchart TD
    call["msgraph.request · msgraph.batch · msgraph.download<br/>one per logical call, covers every attempt<br/>events: msgraph.retry"]
    token["msgraph.token<br/>one per attempt"]
    attempt["GET /v1.0/me/messages/{id}<br/>kind client, one per HTTP attempt"]
    delta["msgraph.delta<br/>one per Delta drain"]
    delta --> call
    call --> token
    call --> attempt
```

| Span | Kind | Started by |
| --- | --- | --- |
| `msgraph.request` | internal | `Client.Do` and everything built on it: `Get`, `Post`, `Patch`, `Delete`, the generic helpers, and each page of `Pages`, `Items`, `Collect` and `First` |
| `msgraph.batch` | internal | `Client.Batch` and `Client.BatchStrict` |
| `msgraph.download` | internal | `Client.Download` |
| `msgraph.delta` | internal | `Delta`, around the `msgraph.request` span of every page |
| `msgraph.token` | internal | each call to the `TokenSource`; duration only |
| `{method} {url.template}` | client | each HTTP attempt, ended when its response body is closed |

`Pages` and `Items` get no span of their own, because the caller's work between
pages would land inside it. The content request of a `Download`, which goes to
a preauthenticated URL outside Graph, is a client span named by its method
alone.

## Attributes

Names follow the OpenTelemetry HTTP client semantic conventions where they
apply; everything else uses the `msgraph.` prefix.

| Attribute | On | Value |
| --- | --- | --- |
| `http.request.method` | call, attempt | `GET`, `POST`, ... |
| `url.template` | call, attempt | redacted route, see below |
| `http.response.status_code` | call, attempt | final status; absent on a transport failure |
| `error.type` | call, attempt, token | see [Errors](#errors) |
| `msgraph.error.code` | call | Graph service code such as `ErrorItemNotFound` |
| `server.address`, `server.port` | attempt | Graph host and port |
| `http.request.resend_count` | attempt, retry event | attempt number; absent on the first attempt |
| `msgraph.http.connection.reused` | attempt | whether the attempt reused a pooled connection |
| `msgraph.http.dns_ms` | attempt | DNS lookup, when one happened |
| `msgraph.http.connect_ms` | attempt | TCP connect, when a connection was dialed |
| `msgraph.http.tls_ms` | attempt | TLS handshake, when one happened |
| `msgraph.http.first_byte_ms` | attempt | from sending the attempt to the first response byte |
| `msgraph.download.content` | attempt | `true` on the preauthenticated content request |
| `msgraph.batch.size`, `msgraph.batch.failed` | batch | subrequests sent, and those answered outside 2xx |
| `msgraph.delta.pages`, `msgraph.delta.items` | delta | pages fetched and items returned |

Connection timings describe the first connection of the attempt. A redirect the
HTTP client follows inside one attempt does not get a span of its own.

`url.full` is deliberately not recorded, though the conventions ask for it:
the query string carries `$filter` values and delta tokens.

### Retry events

Before each wait between attempts the call span gets a `msgraph.retry` event:

| Attribute | Value |
| --- | --- |
| `msgraph.retry.reason` | `throttled` (429), `server_error` (502, 503, 504) or `transport_error` |
| `http.response.status_code` | the status that caused the retry; absent for a transport failure |
| `msgraph.retry.retry_after` | whether the delay came from a `Retry-After` header |
| `msgraph.retry.capped` | whether `WithMaxRetryDelay` shortened it |
| `msgraph.retry.delay_ms` | the planned wait |
| `http.request.resend_count` | the attempt the wait precedes |

## Redaction

Telemetry never contains a token, a header, a request or response body, a
query string, or an error message. Graph error messages can name users and
resources, so errors are recorded only as a classified `error.type`, and
`msgraph.error.code` is kept only when it looks like a service code.

`url.template` keeps a path segment only when it is a known fixed Graph
segment in its position, and renders everything else as `{id}`:

- A fixed segment is an API version, a collection, navigation property or
  action name, a `$` system segment such as `$batch`, or one of a short list
  of `microsoft.graph.*` casts.
- The segment right after a collection (`users`, `messages`, `mailFolders`,
  `events`, `lists`, `items`, ...) holds an identifier or a user-chosen name,
  so it survives only if it is one of that collection's known children:
  `delta`, `$count`, `$ref`, a listed cast, and a few per-collection names
  such as the well-known folders (`inbox`, ...) after `mailFolders` or `root`
  after `sites` and `items`.
- Path-based addressing, from a segment ending in `:` through the next one
  ending in `:` or the end of the path, is a single `{id}`.
- A call keeps an allowlisted name but not its arguments: `delta()`,
  `users({id})`.
- Consecutive placeholders collapse into one, and at most 16 segments are
  rendered; a longer route ends in `/...`.

| Request path | `url.template` |
| --- | --- |
| `/v1.0/me/mailFolders/AQMk.../messages/AAMk...` | `/v1.0/me/mailFolders/{id}/messages/{id}` |
| `/v1.0/users/user@example.com/messages` | `/v1.0/users/{id}/messages` |
| `/v1.0/users('user@example.com')/events` | `/v1.0/users({id})/events` |
| `/v1.0/me/mailFolders/inbox/messages/delta` | `/v1.0/me/mailFolders/inbox/messages/delta` |
| `/v1.0/sites/root/lists/Tasks/items` | `/v1.0/sites/root/lists/{id}/items` |
| `/v1.0/me/drive/root:/Archive/Contacts/Budget.xlsx:/content` | `/v1.0/me/drive/{id}/content` |

The rule fails toward redaction: a fixed segment the lists do not know yet
shows up as `{id}`. What it cannot rule out is a user-chosen name that equals
a collection's known child, such as a list titled `delta`, or a name placed
where Graph expects a navigation rather than an identifier, as in an invalid
path; the lists are maintained by hand.

The preauthenticated content URL of a `Download` names a tenant host and a
signed path, so its span records neither.

## Errors

A failed span has status `Error` and an `error.type` of:

| `error.type` | Meaning |
| --- | --- |
| `404`, `429`, ... | Graph answered with that status |
| `token` | the `TokenSource` failed |
| `transport` | the HTTP request failed below Graph |
| `timeout`, `canceled` | the request timed out or its context was canceled |
| `redirect_refused`, `untrusted_host` | a destination policy refused the request |
| `response_too_large` | the response exceeded `WithMaxResponseBytes` |
| `page_cycle`, `max_pages` | a paged traversal stopped |
| `_OTHER` | anything else |

`ErrNotModified` is an outcome, not a failure, and leaves the status unset.

## Logging

`WithLogger(*slog.Logger)` enables debug-level log lines for what spans do not
already say: currently one line per retry decision. They follow the same
redaction rules.

```text
level=DEBUG msg="msgraph: retrying request" method=GET route=/v1.0/me/events/{id} attempt=1 reason=throttled status=429 retry_after=true capped=false delay=2s
```

## Connection reuse

Clients created without `WithHTTPClient` share one package-level transport,
the way `http.DefaultTransport` is shared, so two clients in one process reuse
each other's connections instead of each paying a TLS handshake. The
transport is unexported, so nothing outside the package can reconfigure it.

A client passed to `WithHTTPClient` is used with its own transport, and
instrumentation reaches that transport through the request context rather
than by replacing it. Reuse one such client, or at least one transport, across
`msgraph.Client` values to keep the pooling.

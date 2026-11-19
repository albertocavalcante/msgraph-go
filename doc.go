// Package msgraph provides a small Microsoft Graph REST client.
//
// The package-level generics are the type-safe entry points: [Get], [Post],
// [Patch], [Do], [Collect], and [First] name the result type at the call site
// instead of passing it through an any out-parameter, and [Pages], [Items],
// and [Delta] iterate collections. The [Client] methods remain available for
// decoding into an existing value, streaming into an io.Writer, or ignoring
// the response body.
//
// Build $filter values with the [Expr] combinators rather than by formatting
// strings, so literals stay escaped. See [Params] for how the typed and raw
// query forms combine.
//
// Requests to hosts other than the base URL's are refused with
// [ErrUntrustedHost], because every request carries a bearer token.
// [WithAllowedHosts] permits additional direct destinations. Redirects remain
// confined to the initial request's origin, are refused for unsafe methods,
// and wrap [ErrRedirectRefused] on policy failure.
//
// # Observability
//
// Tracing is on whenever the request context carries an OpenTelemetry span:
// the client takes its tracer from that span's provider and needs no option.
// Each logical call gets an internal span (msgraph.request, msgraph.batch,
// msgraph.download or msgraph.delta) with a client span per HTTP attempt,
// named "{method} {url.template}", and a msgraph.token span per token
// acquisition. Attempt spans carry connection reuse and DNS, connect, TLS and
// first-byte timings; waits between attempts are msgraph.retry events. Routes
// are redacted to fixed Graph segments with {id} for everything else, and no
// token, header, body, query string or error message is recorded.
// [WithLogger] adds debug log lines for retry decisions. Span names and
// attributes are a stable contract, listed in docs/observability.md.
package msgraph

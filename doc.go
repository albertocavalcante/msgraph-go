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
package msgraph

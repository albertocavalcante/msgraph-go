# msgraph-go

`msgraph-go` is a small, dependency-free Microsoft Graph REST client for Go.
It owns transport concerns that every typed Microsoft SDK in this workspace
needs: bearer-token injection, OData query construction, JSON request/response
handling, paging, throttling retries, and structured Graph errors.

It deliberately does not import `msauth-go`. Callers provide a `TokenSource`,
so CLIs can wire Microsoft delegated auth while tests and services can provide
their own token source.

## Usage

```go
client := msgraph.New(msgraph.TokenSourceFunc(func(ctx context.Context) (string, error) {
    return "access-token", nil
}))

var page msgraph.Page[Message]
_, err := client.Get(ctx, "/me/messages", msgraph.Params{
    Select: []string{"id", "subject", "receivedDateTime"},
    Top:    10,
}, &page)
```

## Develop

```sh
just ci
```

Manual live smoke test:

```sh
cd cmd/msgraph-live-smoke
go run .
go run . # second run should reuse the msauth-go token cache silently
```

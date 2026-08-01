package msgraph

// Page is the standard Microsoft Graph collection response shape.
type Page[T any] struct {
	Value    []T    `json:"value"`
	NextLink string `json:"@odata.nextLink,omitempty"`
}

package msgraph

// Page is the standard Microsoft Graph collection response shape.
type Page[T any] struct {
	Value    []T    `json:"value"`
	NextLink string `json:"@odata.nextLink,omitempty"`
	// DeltaLink is present on the final page of a delta query. Persist it and
	// pass it back to [Deltas] to fetch only what changed since.
	DeltaLink string `json:"@odata.deltaLink,omitempty"`
	// Count is the total matching item count, populated only when the request
	// set [Params.Count] and the endpoint supports it.
	Count *int64 `json:"@odata.count,omitempty"`
}

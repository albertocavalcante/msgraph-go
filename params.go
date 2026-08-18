package msgraph

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ErrInvalidParams is wrapped by [Params.Validate] failures.
var ErrInvalidParams = errors.New("msgraph: invalid query parameters")

// Params holds common OData query parameters used by Microsoft Graph.
//
// Each parameter has a typed form and a raw string form. Prefer the typed
// form: [Params.Where] escapes literals, while [Params.Filter] is passed
// through verbatim and makes the caller responsible for escaping.
type Params struct {
	Select []string
	Expand []string

	// Sort is the typed $orderby. Terms are emitted before OrderBy.
	Sort []Order
	// OrderBy is the raw $orderby, for expressions Sort cannot express.
	OrderBy []string

	// Where is the typed $filter. A nil Where emits no $filter.
	Where Expr
	// Filter is the raw $filter. When both are set they are combined with
	// "and", so a caller can narrow a typed filter with a raw fragment.
	Filter string

	Search string
	Top    int
	Skip   int
	// Count requests $count=true. Most collections also require
	// [ConsistencyLevelEventual] on the request for it to be accepted.
	Count bool
	// SkipToken sets $skiptoken directly. Prefer following @odata.nextLink.
	SkipToken string
	Custom    url.Values
}

// Validate reports whether p can be rendered as a Graph query string.
func (p Params) Validate() error {
	if p.Top < 0 {
		return fmt.Errorf("%w: Top must not be negative, got %d", ErrInvalidParams, p.Top)
	}
	if p.Skip < 0 {
		return fmt.Errorf("%w: Skip must not be negative, got %d", ErrInvalidParams, p.Skip)
	}
	return nil
}

// Values renders p as URL query values. Invalid values are dropped; call
// [Params.Validate] to detect them instead.
func (p Params) Values() url.Values {
	values := url.Values{}
	if len(p.Select) > 0 {
		values.Set("$select", strings.Join(p.Select, ","))
	}
	if len(p.Expand) > 0 {
		values.Set("$expand", strings.Join(p.Expand, ","))
	}
	if orderBy := p.orderByValue(); orderBy != "" {
		values.Set("$orderby", orderBy)
	}
	if p.Search != "" {
		values.Set("$search", p.Search)
	}
	if filter := p.filterValue(); filter != "" {
		values.Set("$filter", filter)
	}
	if p.Top > 0 {
		values.Set("$top", strconv.Itoa(p.Top))
	}
	if p.Skip > 0 {
		values.Set("$skip", strconv.Itoa(p.Skip))
	}
	if p.Count {
		values.Set("$count", "true")
	}
	if p.SkipToken != "" {
		values.Set("$skiptoken", p.SkipToken)
	}
	for key, vals := range p.Custom {
		for _, val := range vals {
			values.Add(key, val)
		}
	}
	return values
}

func (p Params) filterValue() string {
	var typed string
	if p.Where != nil {
		typed = strings.TrimSpace(p.Where.ODataFilter())
	}
	raw := strings.TrimSpace(p.Filter)
	switch {
	case typed == "":
		return raw
	case raw == "":
		return typed
	default:
		return "(" + typed + ") and (" + raw + ")"
	}
}

func (p Params) orderByValue() string {
	parts := make([]string, 0, len(p.Sort)+len(p.OrderBy))
	for _, order := range p.Sort {
		if strings.TrimSpace(order.Field) != "" {
			parts = append(parts, order.String())
		}
	}
	for _, order := range p.OrderBy {
		if strings.TrimSpace(order) != "" {
			parts = append(parts, order)
		}
	}
	return strings.Join(parts, ",")
}

package msgraph

import (
	"net/url"
	"strconv"
	"strings"
)

// Params holds common OData query parameters used by Microsoft Graph.
type Params struct {
	Select  []string
	Expand  []string
	OrderBy []string
	Search  string
	Filter  string
	Top     int
	Skip    int
	Custom  url.Values
}

// Values renders p as URL query values.
func (p Params) Values() url.Values {
	values := url.Values{}
	if len(p.Select) > 0 {
		values.Set("$select", strings.Join(p.Select, ","))
	}
	if len(p.Expand) > 0 {
		values.Set("$expand", strings.Join(p.Expand, ","))
	}
	if len(p.OrderBy) > 0 {
		values.Set("$orderby", strings.Join(p.OrderBy, ","))
	}
	if p.Search != "" {
		values.Set("$search", p.Search)
	}
	if p.Filter != "" {
		values.Set("$filter", p.Filter)
	}
	if p.Top > 0 {
		values.Set("$top", strconv.Itoa(p.Top))
	}
	if p.Skip > 0 {
		values.Set("$skip", strconv.Itoa(p.Skip))
	}
	for key, vals := range p.Custom {
		for _, val := range vals {
			values.Add(key, val)
		}
	}
	return values
}

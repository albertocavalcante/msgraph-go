package msgraph

import (
	"strconv"
	"strings"
	"time"
)

// Expr is a typed OData filter expression. Building filters through Expr
// instead of formatting strings keeps literals escaped: a value containing an
// apostrophe, such as the display name O'Brien, would otherwise terminate the
// OData string literal early and change the meaning of the query.
type Expr interface {
	// ODataFilter renders the expression as a $filter value. The result is not
	// URL-encoded; [Params] handles that.
	ODataFilter() string
}

// Literal constrains the Go types that have an unambiguous OData literal
// encoding. Named types are excluded on purpose so the encoding stays a total
// function over the listed set.
type Literal interface {
	string | bool |
		int | int8 | int16 | int32 | int64 |
		uint | uint8 | uint16 | uint32 | uint64 |
		float32 | float64 |
		time.Time
}

type rawExpr string

func (e rawExpr) ODataFilter() string { return string(e) }

// Raw wraps a hand-written OData fragment. It is the escape hatch for
// expressions this package does not model; the caller owns escaping.
func Raw(filter string) Expr {
	if strings.TrimSpace(filter) == "" {
		return nil
	}
	return rawExpr(filter)
}

type comparisonExpr struct {
	field    string
	operator string
	literal  string
}

func (e comparisonExpr) ODataFilter() string {
	return e.field + " " + e.operator + " " + e.literal
}

func compare[T Literal](field, operator string, value T) Expr {
	return comparisonExpr{field: field, operator: operator, literal: LiteralOf(value)}
}

// Eq builds "field eq value".
func Eq[T Literal](field string, value T) Expr { return compare(field, "eq", value) }

// Ne builds "field ne value".
func Ne[T Literal](field string, value T) Expr { return compare(field, "ne", value) }

// Gt builds "field gt value".
func Gt[T Literal](field string, value T) Expr { return compare(field, "gt", value) }

// Ge builds "field ge value".
func Ge[T Literal](field string, value T) Expr { return compare(field, "ge", value) }

// Lt builds "field lt value".
func Lt[T Literal](field string, value T) Expr { return compare(field, "lt", value) }

// Le builds "field le value".
func Le[T Literal](field string, value T) Expr { return compare(field, "le", value) }

// IsNull builds "field eq null".
func IsNull(field string) Expr {
	return comparisonExpr{field: field, operator: "eq", literal: "null"}
}

// IsNotNull builds "field ne null".
func IsNotNull(field string) Expr {
	return comparisonExpr{field: field, operator: "ne", literal: "null"}
}

type functionExpr struct {
	name  string
	field string
	arg   string
}

func (e functionExpr) ODataFilter() string {
	return e.name + "(" + e.field + "," + e.arg + ")"
}

// Contains builds "contains(field,'value')".
func Contains(field, value string) Expr {
	return functionExpr{name: "contains", field: field, arg: quoteODataString(value)}
}

// StartsWith builds "startswith(field,'value')".
func StartsWith(field, value string) Expr {
	return functionExpr{name: "startswith", field: field, arg: quoteODataString(value)}
}

// EndsWith builds "endswith(field,'value')".
func EndsWith(field, value string) Expr {
	return functionExpr{name: "endswith", field: field, arg: quoteODataString(value)}
}

// In builds "field in (a,b,c)". It returns nil when no values are supplied,
// because an empty OData "in" list is a syntax error rather than a filter that
// matches nothing.
func In[T Literal](field string, values ...T) Expr {
	if len(values) == 0 {
		return nil
	}
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = LiteralOf(value)
	}
	return rawExpr(field + " in (" + strings.Join(parts, ",") + ")")
}

type logicalExpr struct {
	operator string
	operands []Expr
}

func (e logicalExpr) ODataFilter() string {
	parts := make([]string, len(e.operands))
	for i, operand := range e.operands {
		parts[i] = operand.ODataFilter()
	}
	return "(" + strings.Join(parts, " "+e.operator+" ") + ")"
}

func combine(operator string, operands []Expr) Expr {
	kept := make([]Expr, 0, len(operands))
	for _, operand := range operands {
		if operand != nil {
			kept = append(kept, operand)
		}
	}
	switch len(kept) {
	case 0:
		// Dropping a filter entirely is safer than emitting "()" and having
		// Graph reject the whole request.
		return nil
	case 1:
		return kept[0]
	default:
		return logicalExpr{operator: operator, operands: kept}
	}
}

// And joins operands with "and", skipping nil operands.
func And(operands ...Expr) Expr { return combine("and", operands) }

// Or joins operands with "or", skipping nil operands.
func Or(operands ...Expr) Expr { return combine("or", operands) }

type notExpr struct{ operand Expr }

func (e notExpr) ODataFilter() string { return "not (" + e.operand.ODataFilter() + ")" }

// Not negates operand. A nil operand yields nil.
func Not(operand Expr) Expr {
	if operand == nil {
		return nil
	}
	return notExpr{operand: operand}
}

type lambdaExpr struct {
	collection string
	operator   string
	variable   string
	predicate  Expr
}

func (e lambdaExpr) ODataFilter() string {
	return e.collection + "/" + e.operator + "(" + e.variable + ":" + e.predicate.ODataFilter() + ")"
}

// Any builds a collection lambda such as
// "toRecipients/any(r:r/emailAddress/address eq 'a@b.com')". A nil predicate
// yields nil.
func Any(collection, variable string, predicate Expr) Expr {
	return lambda(collection, "any", variable, predicate)
}

// All builds a collection lambda using the "all" operator.
func All(collection, variable string, predicate Expr) Expr {
	return lambda(collection, "all", variable, predicate)
}

func lambda(collection, operator, variable string, predicate Expr) Expr {
	if predicate == nil {
		return nil
	}
	return lambdaExpr{collection: collection, operator: operator, variable: variable, predicate: predicate}
}

// Field joins property segments into an OData path such as
// "from/emailAddress/address".
func Field(segments ...string) string {
	return strings.Join(segments, "/")
}

// LiteralOf renders a Go value as an OData literal.
func LiteralOf[T Literal](value T) string {
	switch typed := any(value).(type) {
	case string:
		return quoteODataString(typed)
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.FormatInt(int64(typed), 10)
	case int8:
		return strconv.FormatInt(int64(typed), 10)
	case int16:
		return strconv.FormatInt(int64(typed), 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint8:
		return strconv.FormatUint(uint64(typed), 10)
	case uint16:
		return strconv.FormatUint(uint64(typed), 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case time.Time:
		// Graph compares DateTimeOffset properties in UTC and rejects a
		// literal carrying a non-UTC offset on several mail endpoints.
		return typed.UTC().Format(time.RFC3339)
	default:
		// Unreachable: the Literal constraint admits no other type.
		return "null"
	}
}

// quoteODataString wraps s in single quotes, doubling any embedded quote as
// OData requires.
func quoteODataString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// Order is one $orderby term.
type Order struct {
	Field      string
	Descending bool
}

// Asc sorts by field ascending.
func Asc(field string) Order { return Order{Field: field} }

// Desc sorts by field descending.
func Desc(field string) Order { return Order{Field: field, Descending: true} }

// String renders the term as an OData $orderby clause.
func (o Order) String() string {
	if o.Descending {
		return o.Field + " desc"
	}
	return o.Field
}

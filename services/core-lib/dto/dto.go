// Package dto holds the request shapes shared by every module: a list query with filters, sorting and paging.
package dto

import (
	"strings"

	rqp "github.com/timsolov/rest-query-parser"
)

// SortOrder is ASC or DESC.
type SortOrder string

const (
	ASC  SortOrder = "ASC"
	DESC SortOrder = "DESC"
)

// Filter is one condition: Field Operator Value, e.g. status EQ "OPEN". Modules validate Field and Operator
// against their own allow-lists before use.
type Filter struct {
	Field    string      `json:"field"`
	Operator string      `json:"operator"`
	Value    interface{} `json:"value"`
}

// Sort orders by one field.
type Sort struct {
	SortBy string    `json:"sortBy"`
	Order  SortOrder `json:"order"`
}

// QueryBuilder is a list request. Limit 0 means no limit.
type QueryBuilder struct {
	Filters []Filter `json:"filters"`
	Sort    []*Sort  `json:"sort"`
	Limit   int      `json:"limit"`
	Offset  int      `json:"offset"`
}

// ToRQP converts the request into a rest-query-parser query for the repository layer. Field names and operators
// are passed through unchanged (operators upper-cased), so callers must check them against their allow-lists
// first: the field becomes a column name in SQL.
func (q *QueryBuilder) ToRQP() *rqp.Query {
	out := rqp.New()
	if q == nil {
		return out
	}
	for _, f := range q.Filters {
		out.AddFilter(f.Field, rqp.Method(strings.ToUpper(f.Operator)), f.Value)
	}
	for _, s := range q.Sort {
		if s != nil {
			out.AddSortBy(s.SortBy, strings.EqualFold(string(s.Order), string(DESC)))
		}
	}
	return out.SetLimit(q.Limit).SetOffset(q.Offset)
}

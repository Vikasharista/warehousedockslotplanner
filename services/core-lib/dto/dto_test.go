package dto

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToRQP(t *testing.T) {
	q := (&QueryBuilder{
		Filters: []Filter{
			{Field: "status", Operator: "eq", Value: "ACTIVE"},
			{Field: "code", Operator: "IN", Value: []interface{}{"a", "b"}},
			{Field: "name", Operator: "ILIKE", Value: "wid*"},
		},
		Sort:   []*Sort{{SortBy: "created_at", Order: DESC}, nil, {SortBy: "id", Order: ASC}},
		Limit:  20,
		Offset: 40,
	}).ToRQP()

	assert.Equal(t, "status = ? AND code IN (?, ?) AND name ILIKE ?", q.Where())
	assert.Equal(t, []interface{}{"ACTIVE", "a", "b", "wid%"}, q.Args())
	assert.Equal(t, "created_at DESC, id", q.Order())
	assert.Equal(t, 20, q.Limit)
	assert.Equal(t, 40, q.Offset)
}

func TestToRQPOnEmptyAndNil(t *testing.T) {
	var nilQuery *QueryBuilder
	for _, q := range []*QueryBuilder{nilQuery, {}} {
		r := q.ToRQP()
		assert.Empty(t, r.Where())
		assert.Empty(t, r.Order())
		assert.Zero(t, r.Limit)
	}
}

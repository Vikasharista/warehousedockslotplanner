package validator

import (
	"testing"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/stretchr/testify/assert"
)

type request struct {
	Name  string `json:"name" validate:"required"`
	Count int    `json:"count" validate:"gte=1"`
	Note  string `validate:"max=3"`
	Skip  string `json:"-" validate:"omitempty,len=2"`
}

func TestValidRequestPasses(t *testing.T) {
	assert.NoError(t, Validator().Validate(request{Name: "a", Count: 1}))
	assert.Same(t, Validator(), Validator())
}

func TestFailuresNameJsonFieldsAndRules(t *testing.T) {
	err := Validator().Validate(request{Count: 0, Note: "long", Skip: "abc"})
	assert.Equal(t, errors.BadRequestErrorCode, errors.CodeOf(err))
	msg := err.Error()
	assert.Contains(t, msg, "name failed required")
	assert.Contains(t, msg, "count failed gte=1")
	assert.Contains(t, msg, "Note failed max=3")
	assert.Contains(t, msg, "Skip failed len=2")
}

func TestNonStructIsABadRequest(t *testing.T) {
	assert.Equal(t, errors.BadRequestErrorCode, errors.CodeOf(Validator().Validate(42)))
}

type pointerRequest struct {
	Price *float64 `json:"unitPrice" validate:"nonzero"`
}

func TestNonzeroMeansPresent(t *testing.T) {
	zero := 0.0
	assert.NoError(t, Validator().Validate(pointerRequest{Price: &zero}))
	err := Validator().Validate(pointerRequest{})
	assert.Contains(t, err.Error(), "unitPrice failed nonzero")
}

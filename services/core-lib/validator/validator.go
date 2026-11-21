// Package validator checks request structs against their `validate` tags (go-playground/validator, plus the
// nonzero alias the bootstrap templates use).
package validator

import (
	"fmt"
	"strings"
	"sync"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	playground "github.com/go-playground/validator/v10"
)

// RequestValidator validates request structs.
type RequestValidator struct {
	v *playground.Validate
}

var (
	once     sync.Once
	instance *RequestValidator
)

// Validator returns the shared validator.
func Validator() *RequestValidator {
	once.Do(func() {
		v := playground.New(playground.WithRequiredStructEnabled())
		v.RegisterTagNameFunc(func(f reflectStructField) string { return jsonName(f.Tag.Get("json"), f.Name) })
		// nonzero (used by the bootstrap templates on required pointer fields): present, i.e. a non-nil
		// pointer, or a non-zero value for non-pointers. A pointer to zero (quantity 0) passes.
		v.RegisterAlias("nonzero", "required")
		instance = &RequestValidator{v: v}
	})
	return instance
}

// Validate returns nil, or BAD_REQUEST naming every failing field and rule.
func (r *RequestValidator) Validate(request interface{}) error {
	err := r.v.Struct(request)
	if err == nil {
		return nil
	}
	var fieldErrs playground.ValidationErrors
	if ok := asValidationErrors(err, &fieldErrs); !ok {
		return errors.NewBadRequestError(err.Error())
	}
	parts := make([]string, 0, len(fieldErrs))
	for _, fe := range fieldErrs {
		if fe.Param() != "" {
			parts = append(parts, fmt.Sprintf("%s failed %s=%s", fe.Field(), fe.Tag(), fe.Param()))
		} else {
			parts = append(parts, fmt.Sprintf("%s failed %s", fe.Field(), fe.Tag()))
		}
	}
	return errors.NewBadRequestError("invalid request: " + strings.Join(parts, "; "))
}

func jsonName(tag, fallback string) string {
	name := strings.Split(tag, ",")[0]
	if name == "" || name == "-" {
		return fallback
	}
	return name
}

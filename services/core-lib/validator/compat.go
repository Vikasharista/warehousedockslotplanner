package validator

import (
	stderrors "errors"
	"reflect"

	playground "github.com/go-playground/validator/v10"
)

type reflectStructField = reflect.StructField

func asValidationErrors(err error, target *playground.ValidationErrors) bool {
	return stderrors.As(err, target)
}

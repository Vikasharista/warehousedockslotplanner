package errors

import (
	stderrors "errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

func TestCodesAreUntypedSoTheyKeyPlainStringMaps(t *testing.T) {
	statusByCode := map[string]int{BadRequestErrorCode: 400, RecordNotFoundErrorCode: 404}
	assert.Equal(t, 404, statusByCode[NewRecordNotFoundError("x").Code()])
}

func TestConstructorsAndCodeOf(t *testing.T) {
	cases := map[string]BaseErr{
		BadRequestErrorCode:            NewBadRequestError("m"),
		RecordNotFoundErrorCode:        NewRecordNotFoundError("m"),
		DuplicateRequestErrorCode:      NewDuplicateRequestError("m"),
		UnauthenticatedErrorCode:       NewUnauthenticatedError("m"),
		UnauthorisedErrorCode:          NewUnauthorisedError("m"),
		ForbiddenErrorCode:             NewForbiddenError("m"),
		InternalServerErrorCode:        NewInternalServerError("m"),
		RequiredHeaderMissingErrorCode: NewRequiredHeaderMissingError("X-Namespace-Id"),
	}
	for code, err := range cases {
		assert.Equal(t, code, err.Code())
		assert.Equal(t, code, CodeOf(fmt.Errorf("wrapped: %w", err)))
	}
	assert.Equal(t, "header X-Namespace-Id is required", NewRequiredHeaderMissingError("X-Namespace-Id").Error())
	assert.Equal(t, InternalServerErrorCode, CodeOf(stderrors.New("plain")))
}

func TestWrapKeepsTheCause(t *testing.T) {
	cause := stderrors.New("driver detail")
	err := Wrap(BadRequestErrorCode, "public", cause)
	assert.Equal(t, "public", err.Error())
	assert.ErrorIs(t, err, cause)
}

func TestRepositoryErrorMapsDatabaseErrors(t *testing.T) {
	assert.NoError(t, RepositoryError(nil))
	assert.Equal(t, RecordNotFoundErrorCode, CodeOf(RepositoryError(gorm.ErrRecordNotFound)))
	for code, want := range map[string]string{
		"23505": DuplicateRequestErrorCode, "23503": BadRequestErrorCode, "23514": BadRequestErrorCode,
		"23502": BadRequestErrorCode, "22P02": BadRequestErrorCode, "22001": BadRequestErrorCode,
		"40001": InternalServerErrorCode,
	} {
		err := RepositoryError(&pgconn.PgError{Code: code, Message: "secret detail"})
		assert.Equal(t, want, CodeOf(err), code)
		assert.NotContains(t, err.Error(), "secret detail")
	}
	known := NewForbiddenError("keep me")
	assert.Same(t, known, RepositoryError(fmt.Errorf("ctx: %w", known)))
	assert.Equal(t, InternalServerErrorCode, CodeOf(RepositoryError(stderrors.New("boom"))))
}

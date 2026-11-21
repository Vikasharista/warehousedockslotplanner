// Package errors is the error vocabulary of every service: a code clients can switch on plus a message.
// Handlers map codes to HTTP statuses and GraphQL extensions; anything that is not a BaseErr is internal.
package errors

import (
	stderrors "errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// Error codes. Untyped string constants, so they work as map keys and in JSON as they are.
const (
	BadRequestErrorCode            = "BAD_REQUEST"
	RequiredHeaderMissingErrorCode = "REQUIRED_HEADER_MISSING"
	RecordNotFoundErrorCode        = "RECORD_NOT_FOUND"
	DuplicateRequestErrorCode      = "DUPLICATE_REQUEST"
	UnauthenticatedErrorCode       = "UNAUTHENTICATED"
	UnauthorisedErrorCode          = "UNAUTHORISED"
	ForbiddenErrorCode             = "FORBIDDEN"
	InternalServerErrorCode        = "INTERNAL_SERVER_ERROR"
)

// BaseErr is an error with a code.
type BaseErr interface {
	error
	Code() string
}

type baseErr struct {
	code    string
	message string
	cause   error
}

func (e *baseErr) Error() string { return e.message }
func (e *baseErr) Code() string  { return e.code }
func (e *baseErr) Unwrap() error { return e.cause }

// New returns an error with the given code and message.
func New(code, message string) BaseErr { return &baseErr{code: code, message: message} }

// Wrap returns an error with the given code and message that unwraps to cause.
func Wrap(code, message string, cause error) BaseErr {
	return &baseErr{code: code, message: message, cause: cause}
}

func NewBadRequestError(message string) BaseErr     { return New(BadRequestErrorCode, message) }
func NewRecordNotFoundError(message string) BaseErr { return New(RecordNotFoundErrorCode, message) }
func NewDuplicateRequestError(message string) BaseErr {
	return New(DuplicateRequestErrorCode, message)
}
func NewUnauthenticatedError(message string) BaseErr { return New(UnauthenticatedErrorCode, message) }
func NewUnauthorisedError(message string) BaseErr    { return New(UnauthorisedErrorCode, message) }
func NewForbiddenError(message string) BaseErr       { return New(ForbiddenErrorCode, message) }
func NewRequiredHeaderMissingError(header string) BaseErr {
	return New(RequiredHeaderMissingErrorCode, fmt.Sprintf("header %s is required", header))
}
func NewInternalServerError(message string) BaseErr { return New(InternalServerErrorCode, message) }

// CodeOf returns the code of err, or InternalServerErrorCode when err carries none.
func CodeOf(err error) string {
	var b BaseErr
	if stderrors.As(err, &b) {
		return b.Code()
	}
	return InternalServerErrorCode
}

// RepositoryError turns a database error into a BaseErr: no rows -> RECORD_NOT_FOUND, unique violation ->
// DUPLICATE_REQUEST, foreign-key / check / not-null violation -> BAD_REQUEST, anything else -> internal.
// The driver's message is kept as the cause, never shown to callers.
func RepositoryError(err error) error {
	if err == nil {
		return nil
	}
	var b BaseErr
	if stderrors.As(err, &b) {
		return b
	}
	if stderrors.Is(err, gorm.ErrRecordNotFound) {
		return Wrap(RecordNotFoundErrorCode, "record not found", err)
	}
	var pg *pgconn.PgError
	if stderrors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			return Wrap(DuplicateRequestErrorCode, "a record with the same unique values already exists", err)
		case "23503":
			return Wrap(BadRequestErrorCode, "a referenced record does not exist", err)
		case "23514", "23502", "22P02", "22001":
			return Wrap(BadRequestErrorCode, "a value is not allowed: "+pg.ConstraintName, err)
		}
	}
	return Wrap(InternalServerErrorCode, "database error", err)
}

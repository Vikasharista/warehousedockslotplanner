// Package utility holds small helpers shared by every module.
package utility

import (
	"context"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/constant"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	rqp "github.com/timsolov/rest-query-parser"
)

// GetPointerToPrimitiveType returns a pointer to a copy of v.
func GetPointerToPrimitiveType[T any](v T) *T { return &v }

// value follows the context indirection: indirectKey names the key that holds the value; without it the
// value is read from directKey.
func value(ctx context.Context, indirectKey, directKey constant.ContextKey) string {
	if ctx == nil {
		return ""
	}
	key := any(directKey)
	if k := ctx.Value(indirectKey); k != nil {
		key = k
	}
	if s, ok := ctx.Value(key).(string); ok {
		return s
	}
	return ""
}

// GetNamespaceID returns the request's namespace, or REQUIRED_HEADER_MISSING when there is none.
func GetNamespaceID(ctx context.Context) (string, error) {
	if ns := value(ctx, constant.CtxNamespaceKey, constant.CtxNamespaceID); ns != "" {
		return ns, nil
	}
	return "", errors.NewRequiredHeaderMissingError(constant.HeaderNamespaceID)
}

// GetUserID returns the request's user, or REQUIRED_HEADER_MISSING when there is none.
func GetUserID(ctx context.Context) (string, error) {
	if user := value(ctx, constant.CtxUserKey, constant.CtxUserID); user != "" {
		return user, nil
	}
	return "", errors.NewRequiredHeaderMissingError(constant.HeaderUserID)
}

// WithNamespace returns ctx carrying namespace the way EnrichContext stores it.
func WithNamespace(ctx context.Context, namespace string) context.Context {
	ctx = context.WithValue(ctx, constant.CtxNamespaceID, namespace)
	return context.WithValue(ctx, constant.CtxNamespaceKey, constant.CtxNamespaceID)
}

// WithUser returns ctx carrying user the way EnrichContext stores it.
func WithUser(ctx context.Context, user string) context.Context {
	ctx = context.WithValue(ctx, constant.CtxUserID, user)
	return context.WithValue(ctx, constant.CtxUserKey, constant.CtxUserID)
}

// AppendNamespaceFilterToQuery restricts query to the request's namespace. It never widens a query: a request
// without a namespace is an error, not an unfiltered read.
func AppendNamespaceFilterToQuery(ctx context.Context, query *rqp.Query) (*rqp.Query, error) {
	namespace, err := GetNamespaceID(ctx)
	if err != nil {
		return nil, err
	}
	if query == nil {
		query = rqp.New()
	}
	return query.AddFilter("namespace", rqp.EQ, namespace), nil
}

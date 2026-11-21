// Package graphql holds the HTTP and gqlgen glue shared by every subgraph.
package graphql

import (
	"context"
	stderrors "errors"
	"net/http"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/constant"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/logger"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/utility"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

// EnrichContext copies X-Namespace-Id, X-User-Id and Authorization from the request into its context, where
// repositories, base models and auth directives read them.
//
// The headers are trusted as sent: the service must sit behind a gateway that sets them, and an auth
// directive must verify the caller (the JWT directive replaces X-User-Id with the token's subject).
func EnrichContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if ns := r.Header.Get(constant.HeaderNamespaceID); ns != "" {
			ctx = utility.WithNamespace(ctx, ns)
		}
		if user := r.Header.Get(constant.HeaderUserID); user != "" {
			ctx = utility.WithUser(ctx, user)
		}
		if auth := r.Header.Get(constant.HeaderAuthorization); auth != "" {
			ctx = context.WithValue(ctx, constant.CtxAuthorization, auth)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// PresentGqlError makes every GraphQL error carry extensions.code: the BaseErr code for known errors,
// gqlgen's own message for query errors, and a bare INTERNAL_SERVER_ERROR (logged, never shown) for the rest,
// including panics.
func PresentGqlError(srv *handler.Server) {
	srv.SetErrorPresenter(presentError)
	srv.SetRecoverFunc(recoverPanic)
}

func presentError(ctx context.Context, err error) *gqlerror.Error {
	presented := graphql.DefaultErrorPresenter(ctx, err)
	var base errors.BaseErr
	switch {
	case stderrors.As(err, &base):
		presented.Message = base.Error()
		setCode(presented, base.Code())
	case presented.Err == nil:
		// A parse or validation error raised by gqlgen itself.
		setCode(presented, errors.BadRequestErrorCode)
	default:
		logger.GetLogger().With(zap.Error(err)).Error("graphql.UnhandledError")
		presented.Message = "internal server error"
		setCode(presented, errors.InternalServerErrorCode)
	}
	return presented
}

func recoverPanic(_ context.Context, rec interface{}) error {
	logger.GetLogger().With(zap.Any("panic", rec)).Error("graphql.Panic")
	return errors.NewInternalServerError("internal server error")
}

func setCode(e *gqlerror.Error, code string) {
	if e.Extensions == nil {
		e.Extensions = map[string]interface{}{}
	}
	e.Extensions["code"] = code
}

package graphql

import (
	"context"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"fmt"
	"strings"

	gql "github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler/testserver"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/constant"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/utility"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func TestEnrichContextCopiesTheHeaders(t *testing.T) {
	var seen context.Context
	h := EnrichContext(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r.Context() }))
	req := httptest.NewRequest(http.MethodPost, "/query", nil)
	req.Header.Set("X-Namespace-Id", "ns-1")
	req.Header.Set("X-User-Id", "u-1")
	req.Header.Set("Authorization", "Bearer t")
	h.ServeHTTP(httptest.NewRecorder(), req)

	ns, err := utility.GetNamespaceID(seen)
	require.NoError(t, err)
	user, err := utility.GetUserID(seen)
	require.NoError(t, err)
	assert.Equal(t, "ns-1", ns)
	assert.Equal(t, "u-1", user)
	assert.Equal(t, "Bearer t", seen.Value(constant.CtxAuthorization))
}

func TestEnrichContextLeavesMissingHeadersUnset(t *testing.T) {
	var seen context.Context
	h := EnrichContext(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r.Context() }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	_, err := utility.GetNamespaceID(seen)
	assert.Error(t, err)
	assert.Nil(t, seen.Value(constant.CtxAuthorization))
}

func present(t *testing.T, err error) *gqlerror.Error {
	t.Helper()
	return presentError(context.Background(), err)
}

func TestKnownErrorsKeepTheirCodeAndMessage(t *testing.T) {
	e := present(t, fmt.Errorf("service: %w", errors.NewRecordNotFoundError("widget 7 not found")))
	assert.Equal(t, "widget 7 not found", e.Message)
	assert.Equal(t, errors.RecordNotFoundErrorCode, e.Extensions["code"])
}

func TestUnknownErrorsAreHidden(t *testing.T) {
	e := present(t, stderrors.New("pq: password authentication failed for user x"))
	assert.Equal(t, "internal server error", e.Message)
	assert.Equal(t, errors.InternalServerErrorCode, e.Extensions["code"])
}

func TestQueryErrorsAreBadRequests(t *testing.T) {
	e := present(t, gqlerror.Errorf("Cannot query field \"nope\""))
	assert.Equal(t, errors.BadRequestErrorCode, e.Extensions["code"])
	assert.Contains(t, e.Message, "nope")
}

func TestPresenterAndRecoverAreInstalledOnTheServer(t *testing.T) {
	srv := testserver.New()
	srv.AddTransport(transport.POST{})
	PresentGqlError(srv.Server)
	srv.AroundFields(func(context.Context, gql.Resolver) (interface{}, error) {
		panic("kaboom")
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"query":"{ name }"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	assert.Contains(t, rec.Body.String(), `"code":"INTERNAL_SERVER_ERROR"`)
	assert.NotContains(t, rec.Body.String(), "kaboom")
}

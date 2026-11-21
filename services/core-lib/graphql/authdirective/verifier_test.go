package authdirective

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/utility"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifierNeedsAURL(t *testing.T) {
	_, err := NewVerifierDirective(" ")
	assert.Error(t, err)
}

func TestVerifierWithoutASessionRefuses(t *testing.T) {
	v, err := NewVerifierDirective("http://127.0.0.1:1")
	require.NoError(t, err)
	_, err = v.Authenticate()(context.Background(), nil, resolver(new(string)))
	assert.Equal(t, errors.UnauthenticatedErrorCode, errors.CodeOf(err))
	_, err = v.VerifyUserPermission("p")(context.Background(), nil, resolver(new(string)))
	assert.Equal(t, errors.UnauthenticatedErrorCode, errors.CodeOf(err))
}

func TestVerifierAsksTheGateway(t *testing.T) {
	var query map[string]string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/permissions/verify", r.URL.Path)
		q := r.URL.Query()
		query = map[string]string{"userId": q.Get("userId"), "namespace": q.Get("namespace"), "permission": q.Get("permission")}
		switch q.Get("permission") {
		case "down":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "junk":
			_, _ = w.Write([]byte("<html>"))
		default:
			_ = json.NewEncoder(w).Encode(map[string]bool{"allowed": q.Get("permission") == "widget:read"})
		}
	}))
	defer gw.Close()
	v, err := NewVerifierDirective(gw.URL + "/")
	require.NoError(t, err)
	ctx := utility.WithUser(utility.WithNamespace(context.Background(), "ns-1"), "u-1")

	var ran string
	_, err = v.Authenticate()(ctx, nil, resolver(&ran))
	require.NoError(t, err)
	assert.Equal(t, "u-1", ran)

	_, err = v.VerifyUserPermission("widget:read")(ctx, nil, resolver(&ran))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"userId": "u-1", "namespace": "ns-1", "permission": "widget:read"}, query)

	_, err = v.VerifyUserPermission("widget:write")(ctx, nil, resolver(new(string)))
	assert.Equal(t, errors.ForbiddenErrorCode, errors.CodeOf(err))

	var base errors.BaseErr
	for _, p := range []string{"down", "junk"} {
		_, err = v.VerifyUserPermission(p)(ctx, nil, resolver(new(string)))
		assert.Error(t, err, p)
		assert.NotErrorAs(t, err, &base, p)
	}
	gw.Close()
	_, err = v.VerifyUserPermission("widget:read")(ctx, nil, resolver(new(string)))
	assert.NotErrorAs(t, err, &base)
}

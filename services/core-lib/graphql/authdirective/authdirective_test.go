package authdirective

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/constant"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/utility"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const secret = "test-secret-at-least-32-bytes-long!!"

func token(t *testing.T, key string, c jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(key))
	require.NoError(t, err)
	return s
}

func request(ns, auth string) context.Context {
	ctx := context.Background()
	if ns != "" {
		ctx = utility.WithNamespace(ctx, ns)
	}
	if auth != "" {
		ctx = context.WithValue(ctx, constant.CtxAuthorization, auth)
	}
	// A spoofed header must be replaced by the verified subject.
	return utility.WithUser(ctx, "spoofed")
}

// resolver records the user it ran as.
func resolver(ran *string) func(ctx context.Context) (interface{}, error) {
	return func(ctx context.Context) (interface{}, error) {
		*ran, _ = utility.GetUserID(ctx)
		return "ok", nil
	}
}

func userClaims(perms ...string) jwt.MapClaims {
	return jwt.MapClaims{"sub": "u-1", "permissions": perms, "exp": time.Now().Add(time.Hour).Unix()}
}

func jwtDirective(t *testing.T, cfg Config) *Directive {
	t.Helper()
	cfg.Provider = ProviderJWT
	if cfg.JWTSecret == "" {
		cfg.JWTSecret = secret
	}
	d, err := New(cfg)
	require.NoError(t, err)
	return d
}

func TestConfigFromViper(t *testing.T) {
	v := viper.New()
	v.Set("auth.provider", "jwt")
	v.Set("auth.jwt.secret", "s")
	v.Set("auth.jwt.issuer", "i")
	v.Set("auth.jwt.audience", "a")
	v.Set("auth.identity.url", "http://id")
	v.Set("auth.permsvc.url", "http://perm")
	v.Set("auth.http.timeout", "2s")
	assert.Equal(t, Config{Provider: "jwt", JWTSecret: "s", JWTIssuer: "i", JWTAudience: "a",
		IdentityURL: "http://id", PermsvcURL: "http://perm", HTTPTimeout: 2 * time.Second}, ConfigFromViper(v, "auth"))
	assert.Equal(t, Config{}, ConfigFromViper(nil, "auth"))
}

func TestNewRejectsBadConfig(t *testing.T) {
	for _, cfg := range []Config{{}, {Provider: "unknown"}, {Provider: ProviderJWT}, {Provider: ProviderPermsvc, IdentityURL: "x"}} {
		_, err := New(cfg)
		assert.Error(t, err, "%+v", cfg)
	}
	_, err := New(Config{Provider: ProviderPermsvc, IdentityURL: "http://127.0.0.1:1", PermsvcURL: "http://127.0.0.1:1"})
	assert.NoError(t, err, "building the permsvc directive must not touch the network")
}

func TestJWTAuthenticatesAndReplacesTheUser(t *testing.T) {
	d := jwtDirective(t, Config{})
	var ran string
	out, err := d.Authenticate()(request("ns-1", "Bearer "+token(t, secret, userClaims())), nil, resolver(&ran))
	require.NoError(t, err)
	assert.Equal(t, "ok", out)
	assert.Equal(t, "u-1", ran)
}

func TestJWTRefusals(t *testing.T) {
	d := jwtDirective(t, Config{JWTIssuer: "setuhaul", JWTAudience: "planner"})
	good := jwt.MapClaims{"sub": "u-1", "iss": "setuhaul", "aud": "planner", "exp": time.Now().Add(time.Hour).Unix()}
	with := func(k string, v interface{}) jwt.MapClaims {
		c := jwt.MapClaims{}
		for kk, vv := range good {
			c[kk] = vv
		}
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
		return c
	}
	none, _ := jwt.NewWithClaims(jwt.SigningMethodNone, good).SignedString(jwt.UnsafeAllowNoneSignatureType)
	cases := map[string]string{
		"no header":      "",
		"not bearer":     "Basic abc",
		"empty bearer":   "Bearer  ",
		"garbage":        "Bearer abc.def.ghi",
		"wrong key":      "Bearer " + token(t, "another-secret-another-secret-xx", good),
		"expired":        "Bearer " + token(t, secret, with("exp", time.Now().Add(-time.Minute).Unix())),
		"no exp":         "Bearer " + token(t, secret, with("exp", nil)),
		"wrong issuer":   "Bearer " + token(t, secret, with("iss", "other")),
		"wrong audience": "Bearer " + token(t, secret, with("aud", "other")),
		"no subject":     "Bearer " + token(t, secret, with("sub", nil)),
		"alg none":       "Bearer " + none,
	}
	for name, auth := range cases {
		var ran string
		_, err := d.Authenticate()(request("ns-1", auth), nil, resolver(&ran))
		assert.Equal(t, errors.UnauthenticatedErrorCode, errors.CodeOf(err), name)
		assert.Empty(t, ran, name)
	}
	_, err := d.Authenticate()(request("ns-1", "Bearer "+token(t, secret, good)), nil, resolver(new(string)))
	assert.NoError(t, err)
}

func TestJWTPermissions(t *testing.T) {
	d := jwtDirective(t, Config{})
	has := d.HasPermission()
	var ran string
	_, err := has(request("ns-1", "Bearer "+token(t, secret, userClaims("widget:read"))), nil, resolver(&ran), "widget:read")
	require.NoError(t, err)
	assert.Equal(t, "u-1", ran)

	_, err = has(request("ns-1", "Bearer "+token(t, secret, userClaims("widget:read"))), nil, resolver(new(string)), "widget:write")
	assert.Equal(t, errors.ForbiddenErrorCode, errors.CodeOf(err))

	_, err = has(request("ns-1", "Bearer "+token(t, secret, userClaims("*"))), nil, resolver(new(string)), "anything")
	assert.NoError(t, err)

	_, err = has(request("ns-1", ""), nil, resolver(new(string)), "widget:read")
	assert.Equal(t, errors.UnauthenticatedErrorCode, errors.CodeOf(err))
}

func TestJWTNamespaceClaim(t *testing.T) {
	d := jwtDirective(t, Config{})
	c := userClaims()
	c["namespaces"] = []string{"ns-1"}
	tok := "Bearer " + token(t, secret, c)
	_, err := d.Authenticate()(request("ns-1", tok), nil, resolver(new(string)))
	assert.NoError(t, err)
	_, err = d.Authenticate()(request("ns-2", tok), nil, resolver(new(string)))
	assert.Equal(t, errors.ForbiddenErrorCode, errors.CodeOf(err))
	_, err = d.Authenticate()(request("", tok), nil, resolver(new(string)))
	assert.Equal(t, errors.RequiredHeaderMissingErrorCode, errors.CodeOf(err))
}

func TestAuthenticateThenHasPermissionVerifiesOnce(t *testing.T) {
	var verifies int
	ids := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifies++
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"userId": "u-9", "permissions": []string{"p"}})
	}))
	defer ids.Close()
	d, err := New(Config{Provider: ProviderPermsvc, IdentityURL: ids.URL, PermsvcURL: ids.URL})
	require.NoError(t, err)
	var ran string
	inner := func(ctx context.Context) (interface{}, error) {
		return d.HasPermission()(ctx, nil, resolver(&ran), "p")
	}
	_, err = d.Authenticate()(request("ns-1", "Bearer x"), nil, inner)
	require.NoError(t, err)
	assert.Equal(t, 1, verifies)
	assert.Equal(t, "u-9", ran)
}

func permsvcServers(t *testing.T, identity, check http.HandlerFunc) *Directive {
	t.Helper()
	ids := httptest.NewServer(identity)
	perms := httptest.NewServer(check)
	t.Cleanup(ids.Close)
	t.Cleanup(perms.Close)
	d, err := New(Config{Provider: ProviderPermsvc, IdentityURL: ids.URL + "/", PermsvcURL: perms.URL})
	require.NoError(t, err)
	return d
}

func TestPermsvcContract(t *testing.T) {
	var gotAuth string
	var gotCheck map[string]string
	d := permsvcServers(t,
		func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/v1/sessions/verify", r.URL.Path)
			assert.Equal(t, http.MethodPost, r.Method)
			gotAuth = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"userId": "u-2"})
		},
		func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/v1/permissions/check", r.URL.Path)
			_ = json.NewDecoder(r.Body).Decode(&gotCheck)
			_ = json.NewEncoder(w).Encode(map[string]bool{"allowed": gotCheck["permission"] == "widget:read"})
		})
	var ran string
	_, err := d.HasPermission()(request("ns-1", "Bearer abc"), nil, resolver(&ran), "widget:read")
	require.NoError(t, err)
	assert.Equal(t, "Bearer abc", gotAuth)
	assert.Equal(t, map[string]string{"userId": "u-2", "namespace": "ns-1", "permission": "widget:read"}, gotCheck)
	assert.Equal(t, "u-2", ran)

	_, err = d.HasPermission()(request("ns-1", "Bearer abc"), nil, resolver(new(string)), "widget:write")
	assert.Equal(t, errors.ForbiddenErrorCode, errors.CodeOf(err))
}

func TestPermsvcRefusalsAndOutages(t *testing.T) {
	refuse := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }
	down := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }
	ok := func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"userId": "u-2"})
	}
	bad := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) }

	_, err := permsvcServers(t, ok, ok).Authenticate()(request("ns-1", ""), nil, resolver(new(string)))
	assert.Equal(t, errors.UnauthenticatedErrorCode, errors.CodeOf(err))

	_, err = permsvcServers(t, refuse, ok).Authenticate()(request("ns-1", "Bearer x"), nil, resolver(new(string)))
	assert.Equal(t, errors.UnauthenticatedErrorCode, errors.CodeOf(err))

	// Outages are plain errors (no code), which the transports map to 503.
	for name, d := range map[string]*Directive{
		"identity down": permsvcServers(t, down, ok),
		"identity junk": permsvcServers(t, bad, ok),
	} {
		_, err = d.Authenticate()(request("ns-1", "Bearer x"), nil, resolver(new(string)))
		var base errors.BaseErr
		assert.Error(t, err, name)
		assert.NotErrorAs(t, err, &base, name)
	}
	_, err = permsvcServers(t, ok, down).HasPermission()(request("ns-1", "Bearer x"), nil, resolver(new(string)), "p")
	var base errors.BaseErr
	assert.Error(t, err)
	assert.NotErrorAs(t, err, &base)

	unreachable, _ := New(Config{Provider: ProviderPermsvc, IdentityURL: "http://127.0.0.1:1", PermsvcURL: "http://127.0.0.1:1",
		HTTPTimeout: time.Second})
	_, err = unreachable.Authenticate()(request("ns-1", "Bearer x"), nil, resolver(new(string)))
	assert.Error(t, err)
	assert.NotErrorAs(t, err, &base)
}

func TestPermsvcNamespaces(t *testing.T) {
	d := permsvcServers(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"userId": "u-2", "namespaces": []string{"ns-1"}})
	}, func(http.ResponseWriter, *http.Request) {})
	_, err := d.Authenticate()(request("ns-2", "Bearer x"), nil, resolver(new(string)))
	assert.Equal(t, errors.ForbiddenErrorCode, errors.CodeOf(err))
}

func TestVerifyIsTheDirectivesCheckForPlainHTTP(t *testing.T) {
	d := jwtDirective(t, Config{})
	ctx, err := d.Verify(request("ns-1", "Bearer "+token(t, secret, userClaims("widget:read"))))
	require.NoError(t, err)
	user, _ := utility.GetUserID(ctx)
	assert.Equal(t, "u-1", user)
	ok, err := d.Allowed(ctx, "widget:read")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, _ = d.Allowed(ctx, "widget:write")
	assert.False(t, ok)
	_, err = d.Verify(request("ns-1", "Bearer nope"))
	assert.Equal(t, errors.UnauthenticatedErrorCode, errors.CodeOf(err))
}

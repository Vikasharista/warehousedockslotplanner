// Package authdirective provides the @authenticate and @hasPermission directives (also used as REST
// middleware by modules).
//
// Providers, chosen by config:
//
//   - jwt: verifies an HS256 bearer token. Claims: sub = user id, permissions = granted permission strings
//     ("*" grants all), namespaces = namespaces the user may act in (optional; when present, X-Namespace-Id
//     must be one of them), plus exp (required), and iss / aud when configured.
//   - permsvc: asks an identity service and a permission service over HTTP:
//     POST <identity.url>/v1/sessions/verify (Authorization forwarded) -> 200 {"userId", "permissions"?, "namespaces"?}
//     POST <permsvc.url>/v1/permissions/check {"userId","namespace","permission"} -> 200 {"allowed": bool}
//
// A refused caller gets UNAUTHENTICATED or FORBIDDEN; an unreachable service is a plain error (mapped to 503).
package authdirective

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/constant"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/utility"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
)

const (
	ProviderJWT     = "jwt"
	ProviderPermsvc = "permsvc"
)

// Config selects and configures the provider.
type Config struct {
	Provider    string
	JWTSecret   string
	JWTIssuer   string
	JWTAudience string
	IdentityURL string
	PermsvcURL  string
	HTTPTimeout time.Duration
}

// ConfigFromViper reads <prefix>.provider, <prefix>.jwt.secret|issuer|audience, <prefix>.identity.url,
// <prefix>.permsvc.url and <prefix>.http.timeout. <prefix>.cache.* is accepted and ignored (no caching yet).
func ConfigFromViper(v *viper.Viper, prefix string) Config {
	if v == nil {
		return Config{}
	}
	key := func(k string) string { return prefix + "." + k }
	return Config{
		Provider:    v.GetString(key("provider")),
		JWTSecret:   v.GetString(key("jwt.secret")),
		JWTIssuer:   v.GetString(key("jwt.issuer")),
		JWTAudience: v.GetString(key("jwt.audience")),
		IdentityURL: v.GetString(key("identity.url")),
		PermsvcURL:  v.GetString(key("permsvc.url")),
		HTTPTimeout: v.GetDuration(key("http.timeout")),
	}
}

// Directive holds the configured checks.
type Directive struct {
	authenticate func(ctx context.Context) (context.Context, error)
	allowed      func(ctx context.Context, permission string) (bool, error)
}

type session struct {
	user        string
	permissions map[string]bool
}

type sessionKey struct{}

// New validates cfg and builds the directive. It makes no network calls.
func New(cfg Config) (*Directive, error) {
	switch cfg.Provider {
	case ProviderJWT:
		if cfg.JWTSecret == "" {
			return nil, fmt.Errorf("authdirective: jwt provider needs jwt.secret")
		}
		return newJWT(cfg), nil
	case ProviderPermsvc:
		if cfg.IdentityURL == "" || cfg.PermsvcURL == "" {
			return nil, fmt.Errorf("authdirective: permsvc provider needs identity.url and permsvc.url")
		}
		return newPermsvc(cfg), nil
	default:
		return nil, fmt.Errorf("authdirective: unknown provider %q (want %q or %q)", cfg.Provider, ProviderJWT, ProviderPermsvc)
	}
}

// Authenticate returns the @authenticate directive: it verifies the caller and puts the verified user on the context.
func (d *Directive) Authenticate() func(ctx context.Context, obj interface{}, next graphql.Resolver) (interface{}, error) {
	return func(ctx context.Context, _ interface{}, next graphql.Resolver) (interface{}, error) {
		authed, err := d.ensure(ctx)
		if err != nil {
			return nil, err
		}
		return next(authed)
	}
}

// HasPermission returns the @hasPermission(permission) directive. It authenticates first when needed.
func (d *Directive) HasPermission() func(ctx context.Context, obj interface{}, next graphql.Resolver, permission string) (interface{}, error) {
	return func(ctx context.Context, _ interface{}, next graphql.Resolver, permission string) (interface{}, error) {
		authed, err := d.ensure(ctx)
		if err != nil {
			return nil, err
		}
		ok, err := d.allowed(authed, permission)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.NewForbiddenError("missing permission " + permission)
		}
		return next(authed)
	}
}

// Verify authenticates the request carried by ctx (its Authorization value, see constant.CtxAuthorization) and
// returns a context with the verified user and permissions, or UNAUTHENTICATED / FORBIDDEN. It is the same check
// the directives run, for plain HTTP middleware.
func (d *Directive) Verify(ctx context.Context) (context.Context, error) {
	return d.ensure(ctx)
}

// Allowed reports whether the verified user in ctx holds permission (call Verify first).
func (d *Directive) Allowed(ctx context.Context, permission string) (bool, error) {
	return d.allowed(ctx, permission)
}

func (d *Directive) ensure(ctx context.Context) (context.Context, error) {
	if _, ok := ctx.Value(sessionKey{}).(*session); ok {
		return ctx, nil
	}
	return d.authenticate(ctx)
}

func bearer(ctx context.Context) (string, error) {
	raw, _ := ctx.Value(constant.CtxAuthorization).(string)
	token, found := strings.CutPrefix(raw, "Bearer ")
	if !found || strings.TrimSpace(token) == "" {
		return "", errors.NewUnauthenticatedError("a bearer token is required")
	}
	return strings.TrimSpace(token), nil
}

func withSession(ctx context.Context, s *session, namespaces []string) (context.Context, error) {
	if len(namespaces) > 0 {
		ns, err := utility.GetNamespaceID(ctx)
		if err != nil {
			return nil, err
		}
		member := false
		for _, n := range namespaces {
			member = member || n == ns
		}
		if !member {
			return nil, errors.NewForbiddenError("no access to namespace " + ns)
		}
	}
	ctx = utility.WithUser(ctx, s.user)
	ctx = context.WithValue(ctx, constant.CtxPermissions, s.permissions)
	return context.WithValue(ctx, sessionKey{}, s), nil
}

func permissionSet(list []string) map[string]bool {
	set := make(map[string]bool, len(list))
	for _, p := range list {
		set[p] = true
	}
	return set
}

func hasPermission(ctx context.Context, permission string) bool {
	s, ok := ctx.Value(sessionKey{}).(*session)
	return ok && (s.permissions["*"] || s.permissions[permission])
}

// --- jwt ---

type claims struct {
	Permissions []string `json:"permissions"`
	Namespaces  []string `json:"namespaces"`
	jwt.RegisteredClaims
}

func newJWT(cfg Config) *Directive {
	opts := []jwt.ParserOption{jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired()}
	if cfg.JWTIssuer != "" {
		opts = append(opts, jwt.WithIssuer(cfg.JWTIssuer))
	}
	if cfg.JWTAudience != "" {
		opts = append(opts, jwt.WithAudience(cfg.JWTAudience))
	}
	parser := jwt.NewParser(opts...)
	secret := []byte(cfg.JWTSecret)
	return &Directive{
		authenticate: func(ctx context.Context) (context.Context, error) {
			raw, err := bearer(ctx)
			if err != nil {
				return nil, err
			}
			var c claims
			if _, err := parser.ParseWithClaims(raw, &c, func(*jwt.Token) (interface{}, error) { return secret, nil }); err != nil {
				return nil, errors.Wrap(errors.UnauthenticatedErrorCode, "invalid or expired token", err)
			}
			if c.Subject == "" {
				return nil, errors.NewUnauthenticatedError("token has no subject")
			}
			return withSession(ctx, &session{user: c.Subject, permissions: permissionSet(c.Permissions)}, c.Namespaces)
		},
		allowed: func(ctx context.Context, permission string) (bool, error) {
			return hasPermission(ctx, permission), nil
		},
	}
}

// --- permsvc ---

func newPermsvc(cfg Config) *Directive {
	timeout := cfg.HTTPTimeout
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	identity := strings.TrimRight(cfg.IdentityURL, "/") + "/v1/sessions/verify"
	check := strings.TrimRight(cfg.PermsvcURL, "/") + "/v1/permissions/check"
	return &Directive{
		authenticate: func(ctx context.Context) (context.Context, error) {
			raw, _ := ctx.Value(constant.CtxAuthorization).(string)
			if raw == "" {
				return nil, errors.NewUnauthenticatedError("credentials are required")
			}
			var out struct {
				UserID      string   `json:"userId"`
				Permissions []string `json:"permissions"`
				Namespaces  []string `json:"namespaces"`
			}
			status, err := postJSON(ctx, client, identity, raw, nil, &out)
			if err != nil {
				return nil, err
			}
			if status == http.StatusUnauthorized || status == http.StatusForbidden || out.UserID == "" {
				return nil, errors.NewUnauthenticatedError("session is not valid")
			}
			return withSession(ctx, &session{user: out.UserID, permissions: permissionSet(out.Permissions)}, out.Namespaces)
		},
		allowed: func(ctx context.Context, permission string) (bool, error) {
			if hasPermission(ctx, permission) {
				return true, nil
			}
			user, err := utility.GetUserID(ctx)
			if err != nil {
				return false, err
			}
			ns, _ := utility.GetNamespaceID(ctx)
			var out struct {
				Allowed bool `json:"allowed"`
			}
			body := map[string]string{"userId": user, "namespace": ns, "permission": permission}
			status, err := postJSON(ctx, client, check, "", body, &out)
			if err != nil {
				return false, err
			}
			return status == http.StatusOK && out.Allowed, nil
		},
	}
}

func postJSON(ctx context.Context, client *http.Client, url, authorization string, body, out interface{}) (int, error) {
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			return 0, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &payload)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set(constant.HeaderAuthorization, authorization)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("authdirective: %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return resp.StatusCode, fmt.Errorf("authdirective: %s answered %d", url, resp.StatusCode)
	}
	if resp.StatusCode == http.StatusOK && out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("authdirective: %s: bad response: %w", url, err)
		}
	}
	return resp.StatusCode, nil
}

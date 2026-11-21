package authdirective

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/utility"
)

// VerifierDirective trusts a gateway that has already authenticated the caller and set X-User-Id, and asks
// the gateway whether that user holds a permission:
// GET <gateway>/v1/permissions/verify?userId=&namespace=&permission= -> 200 {"allowed": bool}.
type VerifierDirective struct {
	client *http.Client
	verify string
}

// NewVerifierDirective builds the directive for the given gateway URL. It makes no network calls.
func NewVerifierDirective(gatewayURL string) (*VerifierDirective, error) {
	if strings.TrimSpace(gatewayURL) == "" {
		return nil, fmt.Errorf("authdirective: gateway URL is required")
	}
	return &VerifierDirective{
		client: &http.Client{Timeout: 3 * time.Second},
		verify: strings.TrimRight(gatewayURL, "/") + "/v1/permissions/verify",
	}, nil
}

// Authenticate requires the gateway-set user on the context.
func (v *VerifierDirective) Authenticate() func(ctx context.Context, obj interface{}, next graphql.Resolver) (interface{}, error) {
	return func(ctx context.Context, _ interface{}, next graphql.Resolver) (interface{}, error) {
		if _, err := utility.GetUserID(ctx); err != nil {
			return nil, errors.NewUnauthenticatedError("no session")
		}
		return next(ctx)
	}
}

// VerifyUserPermission checks permission with the gateway before running next.
func (v *VerifierDirective) VerifyUserPermission(permission string) func(ctx context.Context, obj interface{}, next graphql.Resolver) (interface{}, error) {
	return func(ctx context.Context, _ interface{}, next graphql.Resolver) (interface{}, error) {
		user, err := utility.GetUserID(ctx)
		if err != nil {
			return nil, errors.NewUnauthenticatedError("no session")
		}
		ns, _ := utility.GetNamespaceID(ctx)
		q := url.Values{"userId": {user}, "namespace": {ns}, "permission": {permission}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.verify+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := v.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("authdirective: gateway: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("authdirective: gateway answered %d", resp.StatusCode)
		}
		var out struct {
			Allowed bool `json:"allowed"`
		}
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				return nil, fmt.Errorf("authdirective: gateway: bad response: %w", err)
			}
		}
		if !out.Allowed {
			return nil, errors.NewForbiddenError("missing permission " + permission)
		}
		return next(ctx)
	}
}

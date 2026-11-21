// Package constant holds the context keys and header names shared by every service.
//
// A request context carries the namespace and user twice: the value under CtxNamespaceID / CtxUserID, and
// CtxNamespaceKey / CtxUserKey naming the key that holds it. Readers follow the indirection, so a service can
// move the value under another key without changing its readers.
package constant

// ContextKey is the type of every context key set by core-lib.
type ContextKey string

const (
	CtxNamespaceID  ContextKey = "namespaceId"
	CtxNamespaceKey ContextKey = "namespaceKey"
	CtxUserID       ContextKey = "userId"
	CtxUserKey      ContextKey = "userKey"
	// CtxAuthorization holds the raw Authorization header for the auth directives.
	CtxAuthorization ContextKey = "authorization"
	// CtxPermissions holds the caller's permissions once a directive has authenticated the request.
	CtxPermissions ContextKey = "permissions"
)

const (
	HeaderNamespaceID   = "X-Namespace-Id"
	HeaderUserID        = "X-User-Id"
	HeaderAuthorization = "Authorization"
)

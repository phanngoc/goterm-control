package gateway

import "context"

// Principal identifies the authenticated dashboard user for a request.
// Absent (nil) when auth is disabled — all methods are then allowed.
type Principal struct {
	Username string
	Role     string // "admin" | "viewer"
}

// Admin reports whether the principal may call state-changing methods.
// A nil principal (auth disabled) is treated as admin.
func (p *Principal) Admin() bool { return p == nil || p.Role == "admin" }

type principalKey struct{}

func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

// viewerMethods are the read-only RPC methods a "viewer" may call.
var viewerMethods = map[string]bool{
	"status":         true,
	"models.list":    true,
	"sessions.list":  true,
	"sessions.get":   true,
	"transcript.get": true,
}

// Allowed reports whether the principal may invoke the given method.
func (p *Principal) Allowed(method string) bool {
	return p.Admin() || viewerMethods[method]
}

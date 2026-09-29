// Package auth verifies the bearer tokens of the IdP and holds the permission
// matrix of D-07. It knows neither the use cases nor HTTP routes.
package auth

import "context"

// Role is a client role of the resource server (pda-api) in the token.
type Role string

const (
	RoleProvider       Role = "provider"
	RoleWalletInternal Role = "wallet-internal"
)

// Principal is the authenticated caller. ProviderID comes from the
// provider_id claim, never from the request (AUTH-04).
type Principal struct {
	Subject    string
	ClientID   string
	ProviderID string
	Roles      []Role
}

type principalKey struct{}

// WithPrincipal returns ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext returns the principal of an authenticated request.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

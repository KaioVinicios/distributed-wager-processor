package auth_test

import (
	"context"
	"testing"

	"github.com/KaioVinicios/pda/internal/auth"
)

var (
	providerA = auth.Principal{Subject: "a", ProviderID: "provider-a", Roles: []auth.Role{auth.RoleProvider}}
	providerB = auth.Principal{Subject: "b", ProviderID: "provider-b", Roles: []auth.Role{auth.RoleProvider}}
	internal  = auth.Principal{Subject: "w", Roles: []auth.Role{auth.RoleWalletInternal}}
	noRole    = auth.Principal{Subject: "n"}
	// A provider role without the provider_id claim is a misconfigured client.
	unnamed = auth.Principal{Subject: "u", Roles: []auth.Role{auth.RoleProvider}}
)

// Covers: AUTH-04, AUTH-05, AUTH-06 (U15)
func TestAuthPolicy(t *testing.T) {
	t.Run("roles", func(t *testing.T) {
		cases := []struct {
			p     auth.Principal
			roles []auth.Role
			want  bool
		}{
			{providerA, []auth.Role{auth.RoleProvider}, true},
			{providerA, []auth.Role{auth.RoleWalletInternal}, false},
			{internal, []auth.Role{auth.RoleWalletInternal}, true},
			{internal, []auth.Role{auth.RoleProvider, auth.RoleWalletInternal}, true},
			{noRole, []auth.Role{auth.RoleProvider, auth.RoleWalletInternal}, false},
			{unnamed, []auth.Role{auth.RoleProvider}, false},
			{providerA, nil, false},
		}
		for _, tc := range cases {
			if got := auth.HasAnyRole(tc.p, tc.roles...); got != tc.want {
				t.Errorf("HasAnyRole(%s, %v) = %v, want %v", tc.p.Subject, tc.roles, got, tc.want)
			}
		}
		if !auth.HasRole(internal, auth.RoleWalletInternal) || auth.HasRole(unnamed, auth.RoleProvider) {
			t.Error("HasRole disagrees with HasAnyRole")
		}
	})

	t.Run("acting as a provider", func(t *testing.T) {
		cases := []struct {
			p        auth.Principal
			provider string
			want     bool
		}{
			{providerA, "provider-a", true},
			{providerA, "provider-b", false},
			{providerB, "provider-a", false},
			{internal, "provider-a", false},
			{unnamed, "", false},
			{noRole, "", false},
		}
		for _, tc := range cases {
			if got := auth.ActsAs(tc.p, tc.provider); got != tc.want {
				t.Errorf("ActsAs(%s, %q) = %v, want %v", tc.p.Subject, tc.provider, got, tc.want)
			}
		}
	})

	t.Run("seeing an operation", func(t *testing.T) {
		cases := []struct {
			p        auth.Principal
			provider string // "" = the internal OPENING
			want     bool
		}{
			{providerA, "provider-a", true},
			{providerA, "provider-b", false},
			{providerA, "", false},
			{internal, "provider-a", true},
			{internal, "", true},
			{unnamed, "", false},
			{noRole, "provider-a", false},
		}
		for _, tc := range cases {
			if got := auth.CanSeeTransaction(tc.p, tc.provider); got != tc.want {
				t.Errorf("CanSeeTransaction(%s, %q) = %v, want %v", tc.p.Subject, tc.provider, got, tc.want)
			}
		}
	})

	t.Run("principal in the context", func(t *testing.T) {
		if _, ok := auth.FromContext(context.Background()); ok {
			t.Fatal("FromContext of a bare context reports a principal")
		}
		got, ok := auth.FromContext(auth.WithPrincipal(context.Background(), providerA))
		if !ok || got.ProviderID != "provider-a" {
			t.Fatalf("FromContext = %+v, %v", got, ok)
		}
	})
}

package auth

import "slices"

// HasRole reports whether p holds role. The provider role only counts with
// the provider_id claim: a provider without a name is never authorized
// (spec decision 14).
func HasRole(p Principal, role Role) bool {
	if role == RoleProvider && p.ProviderID == "" {
		return false
	}
	return slices.Contains(p.Roles, role)
}

// HasAnyRole reports whether p holds at least one of roles.
func HasAnyRole(p Principal, roles ...Role) bool {
	return slices.ContainsFunc(roles, func(r Role) bool { return HasRole(p, r) })
}

// ActsAs reports whether p is the provider providerID: the body of POST
// /wagering/transactions and the path of /providers/{providerId}/… must name
// the provider of the token (D-07).
func ActsAs(p Principal, providerID string) bool {
	return HasRole(p, RoleProvider) && p.ProviderID == providerID
}

// CanSeeTransaction reports whether p may read an operation of txProviderID
// ("" for the internal OPENING): the internal service sees every operation, a
// provider only its own. The edge answers 404 otherwise, so the id is not
// revealed.
func CanSeeTransaction(p Principal, txProviderID string) bool {
	return HasRole(p, RoleWalletInternal) || txProviderID != "" && ActsAs(p, txProviderID)
}

package auth

import (
	"context"
	"errors"

	"github.com/coreos/go-oidc/v3/oidc"
)

// observedKeySet is the remote key set of the IdP that tells a failure to
// fetch the keys (the token was not judged) from a verdict on the token
// (D-23). go-oidc v3.21.0 wraps only the fetch failures ("fetching keys %w",
// jwks.go) and IDTokenVerifier.Verify flattens the chain (%v, verify.go), so
// the failure is recorded in the context of the call instead.
type observedKeySet struct{ remote oidc.KeySet }

// fetchFailure is where one verification learns that the keys could not be fetched.
type fetchFailure struct{ err error }

type fetchFailureKey struct{}

func (k observedKeySet) VerifySignature(ctx context.Context, jwt string) ([]byte, error) {
	payload, err := k.remote.VerifySignature(ctx, jwt)
	if err != nil && errors.Unwrap(err) != nil {
		if f, ok := ctx.Value(fetchFailureKey{}).(*fetchFailure); ok {
			f.err = err
		}
	}
	return payload, err
}

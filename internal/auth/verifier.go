package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/KaioVinicios/pda/internal/config"
)

// ErrUnauthenticated reports a missing, malformed, forged or expired token.
// The cause stays in the chain for debug logs; the answer never tells which.
var ErrUnauthenticated = errors.New("auth: unauthenticated")

// ErrKeysUnavailable reports that the keys of the IdP could not be fetched:
// the token was not judged, so the failure is transient (D-23).
var ErrKeysUnavailable = errors.New("auth: identity provider keys unavailable")

// maxJWKSBytes bounds the key set read by CheckKeys.
const maxJWKSBytes = 1 << 20

// nbfLeeway is go-oidc's fixed tolerance on nbf, which AuthenticateAt applies
// the same way (D-07).
const nbfLeeway = 5 * time.Minute

// Verifier checks bearer tokens against the IdP keys (D-07): RS256 only, the
// signature by the JWKS (cached, refetched for an unknown kid), iss, aud and
// exp with a tolerance of OIDC_CLOCK_SKEW. There is no discovery: inside the
// compose network the discovery document names another issuer (spike).
type Verifier struct {
	verifier   *oidc.IDTokenVerifier // exp checked against now − skew (Authenticate)
	atVerifier *oidc.IDTokenVerifier // exp and nbf left to AuthenticateAt
	skew       time.Duration
	jwksURL    string
	audience   string
	client     *http.Client
}

// NewVerifier builds the verifier; client fetches the keys. go-oidc compares
// exp without any tolerance, so the skew is applied by moving its clock back
// (spec decision 13); nbf keeps the library's fixed 5 minutes. Both verifiers
// share one key set, so the keys are fetched and cached once.
func NewVerifier(cfg config.Config, client *http.Client) *Verifier {
	keys := observedKeySet{remote: oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), client), cfg.OIDCJWKSURL)}
	skew := cfg.OIDCClockSkew
	newVerifier := func(c oidc.Config) *oidc.IDTokenVerifier {
		c.ClientID, c.SupportedSigningAlgs = cfg.OIDCAudience, []string{oidc.RS256}
		return oidc.NewVerifier(cfg.OIDCIssuer, keys, &c)
	}
	return &Verifier{
		verifier:   newVerifier(oidc.Config{Now: func() time.Time { return time.Now().Add(-skew) }}),
		atVerifier: newVerifier(oidc.Config{SkipExpiryCheck: true}),
		skew:       skew,
		jwksURL:    cfg.OIDCJWKSURL,
		audience:   cfg.OIDCAudience,
		client:     client,
	}
}

// Authenticate verifies raw and returns its principal (see principal).
// ErrKeysUnavailable when the keys could not be fetched, else ErrUnauthenticated.
func (v *Verifier) Authenticate(ctx context.Context, raw string) (Principal, error) {
	token, err := verify(ctx, v.verifier, raw)
	if err != nil {
		return Principal{}, err
	}
	return v.principal(token)
}

// AuthenticateAt verifies raw as Authenticate does, but evaluates exp (with
// the skew) and nbf (with go-oidc's 5 minutes) at the instant at: the
// SentTimestamp of an SQS message (D-23). A token without exp is refused.
func (v *Verifier) AuthenticateAt(ctx context.Context, raw string, at time.Time) (Principal, error) {
	token, err := verify(ctx, v.atVerifier, raw)
	if err != nil {
		return Principal{}, err
	}
	if token.Expiry.Before(at.Add(-v.skew)) {
		return Principal{}, fmt.Errorf("%w: expired at %s, before %s", ErrUnauthenticated, token.Expiry.UTC(), at.UTC())
	}
	var claims struct {
		NotBefore *json.Number `json:"nbf"`
	}
	if err := token.Claims(&claims); err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrUnauthenticated, err)
	}
	if claims.NotBefore != nil {
		nbf, err := claims.NotBefore.Int64()
		if err != nil {
			return Principal{}, fmt.Errorf("%w: nbf: %w", ErrUnauthenticated, err)
		}
		if time.Unix(nbf, 0).After(at.Add(nbfLeeway)) {
			return Principal{}, fmt.Errorf("%w: not valid before %s", ErrUnauthenticated, time.Unix(nbf, 0).UTC())
		}
	}
	return v.principal(token)
}

// verify runs the go-oidc verification and tells a failure to fetch the keys
// (ErrKeysUnavailable) from a verdict on the token (ErrUnauthenticated).
func verify(ctx context.Context, verifier *oidc.IDTokenVerifier, raw string) (*oidc.IDToken, error) {
	failure := &fetchFailure{}
	token, err := verifier.Verify(context.WithValue(ctx, fetchFailureKey{}, failure), raw)
	switch {
	case err == nil:
		return token, nil
	case failure.err != nil:
		return nil, fmt.Errorf("%w: %w", ErrKeysUnavailable, failure.err)
	default:
		return nil, fmt.Errorf("%w: %w", ErrUnauthenticated, err)
	}
}

// principal reads the subject, the client (azp), the provider_id claim and
// the client roles granted on the audience (resource_access.<audience>.roles).
func (v *Verifier) principal(token *oidc.IDToken) (Principal, error) {
	var claims struct {
		AuthorizedParty string `json:"azp"`
		ProviderID      string `json:"provider_id"`
		ResourceAccess  map[string]struct {
			Roles []string `json:"roles"`
		} `json:"resource_access"`
	}
	if err := token.Claims(&claims); err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrUnauthenticated, err)
	}
	p := Principal{Subject: token.Subject, ClientID: claims.AuthorizedParty, ProviderID: claims.ProviderID}
	for _, r := range claims.ResourceAccess[v.audience].Roles {
		p.Roles = append(p.Roles, Role(r))
	}
	return p, nil
}

// CheckKeys fetches the key set once and requires an RSA key: the fail-fast
// check of the start (FX-02, spec decision 15).
func (v *Verifier) CheckKeys(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return fmt.Errorf("auth: JWKS request: %w", err)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("auth: fetching the JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: the JWKS endpoint answered %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSBytes)).Decode(&set); err != nil {
		return fmt.Errorf("auth: decoding the JWKS: %w", err)
	}
	for _, k := range set.Keys {
		if k.Kty == "RSA" {
			return nil
		}
	}
	return errors.New("auth: the JWKS has no RSA key")
}

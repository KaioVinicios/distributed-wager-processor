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

// maxJWKSBytes bounds the key set read by CheckKeys.
const maxJWKSBytes = 1 << 20

// Verifier checks bearer tokens against the IdP keys (D-07): RS256 only, the
// signature by the JWKS (cached, refetched for an unknown kid), iss, aud and
// exp with a tolerance of OIDC_CLOCK_SKEW. There is no discovery: inside the
// compose network the discovery document names another issuer (spike).
type Verifier struct {
	verifier *oidc.IDTokenVerifier
	jwksURL  string
	audience string
	client   *http.Client
}

// NewVerifier builds the verifier; client fetches the keys. go-oidc compares
// exp without any tolerance, so the skew is applied by moving its clock back
// (spec decision 13); nbf keeps the library's fixed 5 minutes.
func NewVerifier(cfg config.Config, client *http.Client) *Verifier {
	keys := oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), client), cfg.OIDCJWKSURL)
	skew := cfg.OIDCClockSkew
	return &Verifier{
		verifier: oidc.NewVerifier(cfg.OIDCIssuer, keys, &oidc.Config{
			ClientID:             cfg.OIDCAudience,
			SupportedSigningAlgs: []string{oidc.RS256},
			Now:                  func() time.Time { return time.Now().Add(-skew) },
		}),
		jwksURL:  cfg.OIDCJWKSURL,
		audience: cfg.OIDCAudience,
		client:   client,
	}
}

// Authenticate verifies raw and returns its principal: the subject, the
// client (azp), the provider_id claim and the client roles granted on the
// audience (resource_access.<audience>.roles).
func (v *Verifier) Authenticate(ctx context.Context, raw string) (Principal, error) {
	token, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrUnauthenticated, err)
	}
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

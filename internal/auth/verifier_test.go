package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/KaioVinicios/pda/internal/auth"
	"github.com/KaioVinicios/pda/internal/config"
)

const (
	issuer   = "http://idp.test/realms/pda"
	audience = "pda-api"
)

// idp serves a JWKS with one RSA key and signs tokens with it.
type idp struct {
	key    *rsa.PrivateKey
	server *httptest.Server
	status atomic.Int32 // answer of the JWKS endpoint; 0 = 200
	keys   []jose.JSONWebKey
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	i := &idp{key: key}
	i.keys = []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}
	i.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if s := i.status.Load(); s != 0 {
			w.WriteHeader(int(s))
			return
		}
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: i.keys})
	}))
	t.Cleanup(i.server.Close)
	return i
}

func (i *idp) verifier(skew time.Duration) *auth.Verifier {
	return auth.NewVerifier(config.Config{
		OIDCIssuer: issuer, OIDCJWKSURL: i.server.URL, OIDCAudience: audience, OIDCClockSkew: skew,
	}, i.server.Client())
}

// claims of a provider-a token that expires in 5 minutes.
func claims() map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": issuer, "aud": audience, "sub": "service-account-a", "azp": "provider-a",
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "provider_id": "provider-a",
		"resource_access": map[string]any{audience: map[string]any{"roles": []string{"provider"}}},
	}
}

func sign(t *testing.T, alg jose.SignatureAlgorithm, key any, kid string, c map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: jose.JSONWebKey{Key: key, KeyID: kid}}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(signer).Claims(c).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// unsigned is a token with alg "none".
func unsigned(t *testing.T, c map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc(payload) + "."
}

// with sets one claim.
func with(c map[string]any, key string, value any) map[string]any {
	c[key] = value
	return c
}

// Covers: AUTH-02, AUTH-04 (U16)
func TestVerifier(t *testing.T) {
	i := newIDP(t)
	v := i.verifier(30 * time.Second)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("accepts a valid token and reads the principal", func(t *testing.T) {
		p, err := v.Authenticate(t.Context(), sign(t, jose.RS256, i.key, "k1", claims()))
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
		if p.Subject != "service-account-a" || p.ClientID != "provider-a" || p.ProviderID != "provider-a" ||
			!slices.Equal(p.Roles, []auth.Role{auth.RoleProvider}) {
			t.Fatalf("principal = %+v", p)
		}
	})

	t.Run("reads the roles of the audience only", func(t *testing.T) {
		c := with(with(claims(), "provider_id", nil), "resource_access", map[string]any{
			audience: map[string]any{"roles": []string{"wallet-internal"}},
			"other":  map[string]any{"roles": []string{"provider"}},
		})
		p, err := v.Authenticate(t.Context(), sign(t, jose.RS256, i.key, "k1", c))
		if err != nil || p.ProviderID != "" || !slices.Equal(p.Roles, []auth.Role{auth.RoleWalletInternal}) {
			t.Fatalf("principal = %+v, %v", p, err)
		}
	})

	t.Run("accepts a token expired within the clock skew", func(t *testing.T) {
		c := with(claims(), "exp", time.Now().Add(-10*time.Second).Unix())
		if _, err := v.Authenticate(t.Context(), sign(t, jose.RS256, i.key, "k1", c)); err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
	})

	rejected := map[string]string{
		"malformed":               "not-a-jwt",
		"empty":                   "",
		"signed by another key":   sign(t, jose.RS256, other, "k1", claims()),
		"unknown key id":          sign(t, jose.RS256, other, "k2", claims()),
		"alg none":                unsigned(t, claims()),
		"HS256":                   sign(t, jose.HS256, []byte("a-shared-secret-of-at-least-32-bytes"), "k1", claims()),
		"another issuer":          sign(t, jose.RS256, i.key, "k1", with(claims(), "iss", "http://idp.test/realms/other")),
		"another audience":        sign(t, jose.RS256, i.key, "k1", with(claims(), "aud", "account")),
		"expired beyond the skew": sign(t, jose.RS256, i.key, "k1", with(claims(), "exp", time.Now().Add(-40*time.Second).Unix())),
	}
	for name, raw := range rejected {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, err := v.Authenticate(t.Context(), raw); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("Authenticate = %v, want ErrUnauthenticated", err)
			}
		})
	}

	t.Run("rejects an expired token when the skew is zero", func(t *testing.T) {
		c := with(claims(), "exp", time.Now().Add(-time.Second).Unix())
		if _, err := i.verifier(0).Authenticate(t.Context(), sign(t, jose.RS256, i.key, "k1", c)); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("Authenticate = %v, want ErrUnauthenticated", err)
		}
	})
}

// Covers: FX-02
func TestVerifierCheckKeys(t *testing.T) {
	t.Run("a key set with an RSA key", func(t *testing.T) {
		if err := newIDP(t).verifier(0).CheckKeys(t.Context()); err != nil {
			t.Fatalf("CheckKeys = %v", err)
		}
	})
	t.Run("an error answer", func(t *testing.T) {
		i := newIDP(t)
		i.status.Store(http.StatusServiceUnavailable)
		if err := i.verifier(0).CheckKeys(t.Context()); err == nil {
			t.Fatal("CheckKeys = nil, want an error")
		}
	})
	t.Run("an empty key set", func(t *testing.T) {
		i := newIDP(t)
		i.keys = nil
		if err := i.verifier(0).CheckKeys(t.Context()); err == nil {
			t.Fatal("CheckKeys = nil, want an error")
		}
	})
	t.Run("an unreachable endpoint", func(t *testing.T) {
		i := newIDP(t)
		i.server.Close()
		if err := i.verifier(0).CheckKeys(t.Context()); err == nil {
			t.Fatal("CheckKeys = nil, want an error")
		}
	})
}

// without deletes one claim.
func without(c map[string]any, key string) map[string]any {
	delete(c, key)
	return c
}

// Covers: AUTH-02, D-23 (U33)
func TestAuthenticateAt(t *testing.T) {
	i := newIDP(t)
	v := i.verifier(30 * time.Second)
	sent := time.Now().Add(-time.Hour)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	// at is a provider-a token issued a minute before instant, valid for 4 minutes after it.
	at := func(instant time.Time) map[string]any {
		return with(with(claims(), "iat", instant.Add(-time.Minute).Unix()), "exp", instant.Add(4*time.Minute).Unix())
	}

	t.Run("accepts a token valid at the instant and expired now", func(t *testing.T) {
		raw := sign(t, jose.RS256, i.key, "k1", at(sent))
		p, err := v.AuthenticateAt(t.Context(), raw, sent)
		if err != nil || p.ProviderID != "provider-a" || !slices.Equal(p.Roles, []auth.Role{auth.RoleProvider}) {
			t.Fatalf("AuthenticateAt = %+v, %v", p, err)
		}
		if _, err := v.Authenticate(t.Context(), raw); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("Authenticate now = %v, want the token expired", err)
		}
	})

	accepted := map[string]map[string]any{
		"expired within the skew at the instant": with(at(sent), "exp", sent.Add(-10*time.Second).Unix()),
		"with nbf within go-oidc's 5 minutes":    with(at(sent), "nbf", sent.Add(4*time.Minute).Unix()),
	}
	for name, c := range accepted {
		t.Run("accepts a token "+name, func(t *testing.T) {
			if _, err := v.AuthenticateAt(t.Context(), sign(t, jose.RS256, i.key, "k1", c), sent); err != nil {
				t.Fatalf("AuthenticateAt = %v", err)
			}
		})
	}

	rejected := map[string]string{
		"expired beyond the skew at the instant": sign(t, jose.RS256, i.key, "k1", with(at(sent), "exp", sent.Add(-40*time.Second).Unix())),
		"without exp":                            sign(t, jose.RS256, i.key, "k1", without(at(sent), "exp")),
		"not valid yet at the instant":           sign(t, jose.RS256, i.key, "k1", with(at(sent), "nbf", sent.Add(6*time.Minute).Unix())),
		"signed by another key":                  sign(t, jose.RS256, other, "k1", at(sent)),
		"another issuer":                         sign(t, jose.RS256, i.key, "k1", with(at(sent), "iss", "http://idp.test/realms/other")),
		"another audience":                       sign(t, jose.RS256, i.key, "k1", with(at(sent), "aud", "account")),
		"alg none":                               unsigned(t, at(sent)),
		"malformed":                              "not-a-jwt",
	}
	for name, raw := range rejected {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, err := v.AuthenticateAt(t.Context(), raw, sent); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("AuthenticateAt = %v, want ErrUnauthenticated", err)
			}
		})
	}
}

// Covers: SQS-07, D-23 (U34)
// Sensitivity: NewVerifier with the RemoteKeySet itself, without observedKeySet → "Authenticate with the JWKS answering 503 = auth: unauthenticated: …".
func TestVerifierKeysUnavailable(t *testing.T) {
	i := newIDP(t)
	v := i.verifier(30 * time.Second) // no key cached yet: the first verification fetches them
	raw := sign(t, jose.RS256, i.key, "k1", claims())

	i.status.Store(http.StatusServiceUnavailable)
	if _, err := v.Authenticate(t.Context(), raw); !errors.Is(err, auth.ErrKeysUnavailable) || errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("Authenticate with the JWKS answering 503 = %v, want ErrKeysUnavailable only", err)
	}
	if _, err := v.AuthenticateAt(t.Context(), raw, time.Now()); !errors.Is(err, auth.ErrKeysUnavailable) {
		t.Fatalf("AuthenticateAt with the JWKS answering 503 = %v, want ErrKeysUnavailable", err)
	}

	i.status.Store(0)
	// go-oidc clears a finished fetch just after answering it: a call right
	// away may still get that answer, so the recovery gets a second.
	for deadline := time.Now().Add(time.Second); ; time.Sleep(10 * time.Millisecond) {
		_, err := v.AuthenticateAt(t.Context(), raw, time.Now())
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("AuthenticateAt with the JWKS back = %v, want the token accepted", err)
		}
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	forged := sign(t, jose.RS256, other, "k9", claims()) // an unknown kid: the keys are fetched again and answer
	if _, err := v.Authenticate(t.Context(), forged); !errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, auth.ErrKeysUnavailable) {
		t.Fatalf("Authenticate of a forged token = %v, want ErrUnauthenticated only", err)
	}

	// A shutdown in the middle of a fetch: transient, with context.Canceled in
	// the chain, so the consumer releases the message (Review Focus 4).
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := i.verifier(30*time.Second).AuthenticateAt(canceled, raw, time.Now()); !errors.Is(err, auth.ErrKeysUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("AuthenticateAt with a canceled context = %v, want ErrKeysUnavailable wrapping context.Canceled", err)
	}

	i.server.Close()
	if _, err := i.verifier(30*time.Second).Authenticate(t.Context(), raw); !errors.Is(err, auth.ErrKeysUnavailable) {
		t.Fatalf("Authenticate with the JWKS unreachable = %v, want ErrKeysUnavailable", err)
	}
}

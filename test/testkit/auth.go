package testkit

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/KaioVinicios/pda/internal/auth"
	"github.com/KaioVinicios/pda/internal/config"
)

// KeycloakURL is the compose Keycloak seen from the host.
const KeycloakURL = "http://localhost:8080"

// KeycloakIssuer is the iss of the realm pda: with KC_HOSTNAME it is the same
// seen from the host and from the compose network (D-07).
const KeycloakIssuer = KeycloakURL + "/realms/pda"

// tokenMargin renews a cached token this long before it expires.
const tokenMargin = 30 * time.Second

type cachedToken struct {
	raw     string
	expires time.Time
}

var (
	tokenMu     sync.Mutex
	tokenCache  = map[string]cachedToken{}
	tokenClient = &http.Client{Timeout: 10 * time.Second}
)

// Token returns a real access token of clientID in the realm pda, by
// client_credentials with the secret of .env.example, cached until shortly
// before it expires.
func Token(tb testing.TB, clientID string) string {
	tb.Helper()
	return cachedTokenOf(tb, "pda", clientID)
}

// OtherRealmToken is a valid token of the realm other: another issuer and
// other keys.
func OtherRealmToken(tb testing.TB) string {
	tb.Helper()
	return cachedTokenOf(tb, "other", "other-provider")
}

// FreshToken requests a new token of clientID in the realm pda, bypassing the
// cache (the expiry test needs one issued now).
func FreshToken(tb testing.TB, clientID string) string {
	tb.Helper()
	raw, _, err := requestToken(context.WithoutCancel(tb.Context()), "pda", clientID)
	if err != nil {
		tb.Fatal(err)
	}
	return raw
}

// TokenExpiry is the exp of a token.
func TokenExpiry(tb testing.TB, raw string) time.Time {
	tb.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		tb.Fatal("testkit: the token is not a JWS")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		tb.Fatal(err)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		tb.Fatal(err)
	}
	return time.Unix(claims.Exp, 0)
}

// OIDCConfig sets on cfg the OIDC settings of the application under test: the
// realm pda of the compose Keycloak, seen from the host, with a 1 s skew.
func OIDCConfig(cfg config.Config) config.Config {
	cfg.OIDCIssuer, cfg.OIDCJWKSURL = KeycloakIssuer, KeycloakIssuer+"/protocol/openid-connect/certs"
	cfg.OIDCAudience, cfg.OIDCClockSkew = "pda-api", time.Second
	return cfg
}

// NewVerifier is the verifier of the application under test (OIDCConfig).
func NewVerifier(tb testing.TB) *auth.Verifier {
	tb.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	tb.Cleanup(client.CloseIdleConnections)
	return auth.NewVerifier(OIDCConfig(config.Config{}), client)
}

func cachedTokenOf(tb testing.TB, realm, clientID string) string {
	tb.Helper()
	tokenMu.Lock()
	defer tokenMu.Unlock()
	key := realm + "/" + clientID
	if t, ok := tokenCache[key]; ok && time.Until(t.expires) > tokenMargin {
		return t.raw
	}
	raw, expires, err := requestToken(context.WithoutCancel(tb.Context()), realm, clientID)
	if err != nil {
		tb.Fatal(err)
	}
	tokenCache[key] = cachedToken{raw: raw, expires: expires}
	return raw
}

func requestToken(ctx context.Context, realm, clientID string) (string, time.Time, error) {
	vals, err := LoadDotEnv()
	if err != nil {
		return "", time.Time{}, err
	}
	secret := vals[strings.ToUpper(strings.ReplaceAll(clientID, "-", "_"))+"_SECRET"]
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {secret}}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		KeycloakURL+"/realms/"+realm+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	issued := time.Now()
	resp, err := tokenClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("testkit: token of %s/%s: %w", realm, clientID, err)
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("testkit: token of %s/%s: status %d", realm, clientID, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("testkit: token of %s/%s: no access token: %w", realm, clientID, err)
	}
	return out.AccessToken, issued.Add(time.Duration(out.ExpiresIn) * time.Second), nil
}

// providerClaims are the claims of a real provider-a token: a forged token
// differs from a valid one only in how it is signed.
func providerClaims(tb testing.TB) map[string]any {
	tb.Helper()
	parts := strings.Split(Token(tb, "provider-a"), ".")
	if len(parts) != 3 {
		tb.Fatal("testkit: the provider-a token is not a JWS")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		tb.Fatalf("testkit: token payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		tb.Fatalf("testkit: token claims: %v", err)
	}
	return claims
}

func signClaims(tb testing.TB, alg jose.SignatureAlgorithm, key any, claims map[string]any) string {
	tb.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: jose.JSONWebKey{Key: key, KeyID: "forged"}},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		tb.Fatal(err)
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		tb.Fatal(err)
	}
	return raw
}

// ForgedToken has the claims of a real provider-a token, signed by an RSA key
// the IdP never published.
func ForgedToken(tb testing.TB) string {
	tb.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		tb.Fatal(err)
	}
	return signClaims(tb, jose.RS256, key, providerClaims(tb))
}

// HS256Token has the claims of a real provider-a token, signed with HS256.
func HS256Token(tb testing.TB) string {
	tb.Helper()
	return signClaims(tb, jose.HS256, []byte("a-shared-secret-of-at-least-32-bytes"), providerClaims(tb))
}

// UnsignedToken has the claims of a real provider-a token and alg "none".
func UnsignedToken(tb testing.TB) string {
	tb.Helper()
	payload, err := json.Marshal(providerClaims(tb))
	if err != nil {
		tb.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc(payload) + "."
}

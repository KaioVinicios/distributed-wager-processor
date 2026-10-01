# Identidade do provedor no SQS: plano de implementação

> **Para quem executa:** execução **inline** com `superpowers:executing-plans` e `superpowers:test-driven-development` em cada tarefa ([`development-workflow.md`](../../development-workflow.md) §3; subagentes só com pedido explícito). **Sem passos de commit:** os commits são propostos no fim (Tarefa 8), e o autor autoriza. Commits deste repositório **não** levam trailer de coautoria de IA.

**Objetivo:** no SQS, como no HTTP, o provedor passa a ser o da identidade autenticada pelo IdP. Uma mensagem sem token válido de provedor, ou que nomeia outro provedor, vai para a DLQ sem nenhum efeito (fecha o E2 no SQS).

**Arquitetura:**
- **`auth`:** ganha `AuthenticateAt` (validade avaliada num instante) e `ErrKeysUnavailable`, apoiado num `KeySet` que registra as falhas de busca do JWKS.
- **`sqsconsumer`:** autentica o atributo `accessToken` no `SentTimestamp`, lê o envelope, compara o provedor e só então chama o caso de uso.
- **`bootstrap`:** inclui o `auth` também para o papel de consumidor.
- **`testkit`:** anexa o token por padrão.
- **`app.ConsumeWager`:** não muda.

**Stack:** Go 1.27.1, Uber Fx, go-oidc v3.21.0, AWS SDK v2. Testes unitários e com a tag `integration` (PostgreSQL, Keycloak e MiniStack reais); regressão com a tag `e2e`.

**Spec:** [`dev/specs/2026-10-01-sqs-provider-auth-design.md`](../specs/2026-10-01-sqs-provider-auth-design.md) (aprovada em 01/10/2026). Decisão: D-23.

## Restrições globais

- **Gates:** `make check` verde ao fim de cada tarefa de código; `make test-integration` verde nas Tarefas 4 a 6 e `make test-e2e` na Tarefa 7. Antes de qualquer teste com tag, `make infra-up`.
- **Comentários nos testes:** `// Covers: …` acima de cada teste novo; `// Sensitivity: …` nos testes escritos sobre comportamento que já existe (sabotar, ver falhar, desfazer).
- **Red:** é uma **asserção** falhando, com stubs de assinatura correta; um erro de compilação sozinho não conta.
- **Atributo:** nome exato `accessToken`, `DataType = String`, valor = o JWT sem `Bearer`.
- **Códigos de rejeição:** `UNAUTHENTICATED`, `FORBIDDEN`, `PROVIDER_MISMATCH`, todos com a categoria `CORRECTABLE`. Razões em `auth_failures_total`: `unauthenticated`, `forbidden`, `provider_mismatch`.
- **Instante da validade:** `exp ≥ SentTimestamp − OIDC_CLOCK_SKEW`; `nbf ≤ SentTimestamp + 5 min`. Sem `SentTimestamp` legível, vale o instante do recebimento.
- **Segredos:** nenhum log e nenhuma cópia explícita na DLQ carregam o token (OBS-02).
- **Lint:** sem `float32`/`float64` (`forbidigo`); chaves de log em `camelCase` com mensagem estática (`sloglint`); nunca `fmt.Print*`.

## Foco da revisão

1. **`accessToken` presente, mas vazio:** `UNAUTHENTICATED`, sem chamar o IdP (U35, Tarefa 4).
2. **`SentTimestamp` ausente ou ilegível:** vale o instante do recebimento, ou seja, a validação falha fechada (U35, Tarefa 4).
3. **Token copiado para a DLQ:** a cópia explícita não pode levar o `accessToken` (`TestDLQInput` estendido e A05, Tarefa 4).
4. **Shutdown no meio de uma busca de chaves:** `ErrKeysUnavailable`, com `context.Canceled` na cadeia, para o `decide` liberar a mensagem em vez de mandá-la à DLQ (U34, Tarefa 2).
5. **Token sem `exp`:** recusado pelo `AuthenticateAt`, que desliga a checagem de expiração do go-oidc (U33, Tarefa 2).

---

## Mapa de arquivos

| Arquivo | Mudança | Tarefa |
| --- | --- | --- |
| `test/testkit/sqs.go` | `SendOpts.Token`/`NoToken`, `tokenFor`, `BodyProviderID`; `SendMessage` anexa o `accessToken` | 1 |
| `test/testkit/sqs_test.go` (novo) | `TestTokenFor` | 1 |
| `test/integration/harness_test.go` | os envios à fila de auditoria com `NoToken` | 1 |
| `internal/auth/keyset.go` (novo) | `observedKeySet`, `fetchFailure` | 2 |
| `internal/auth/verifier.go` | `ErrKeysUnavailable`, `AuthenticateAt`, `verify`, `principal`, dois `IDTokenVerifier` | 2 |
| `internal/auth/verifier_test.go` | `idp.status` atômico; U33, U34 | 2 |
| `internal/bootstrap/bootstrap.go` | `auth.Module` se HTTP **ou** consumidor | 3 |
| `internal/bootstrap/bootstrap_test.go` | caso novo no `TestOptionsFor` | 3 |
| `internal/adapters/sqsconsumer/authorize.go` (novo) | `AccessTokenAttribute`, `Authenticator`, `authenticate`, `matchProvider`, `sentAt`, `refused`, `countRefusal` | 4 |
| `internal/adapters/sqsconsumer/consumer.go` | campo `authn`, `NewConsumer`, `Metrics.AuthFailure`, atributos do `ReceiveMessage`, `handle` | 4 |
| `internal/adapters/sqsconsumer/handler.go` | `decide`: `KindForbidden` → DLQ | 4 |
| `internal/adapters/sqsconsumer/module.go` | injeta o `*auth.Verifier` | 4 |
| `internal/adapters/sqsconsumer/authorize_test.go` (novo) | U35 | 4 |
| `internal/adapters/sqsconsumer/handler_test.go` | linha nova no `TestDecide`; `TestDLQInput` com o token | 4 |
| `internal/adapters/sqsconsumer/consumer_test.go` | `trustingAuth`, `message`, `unitOptions`, `countingMetrics.AuthFailure` | 4 |
| `internal/adapters/sqsconsumer/helpers_integration_test.go` | `consumerOpts.auth`, `trustingAuth`, `send` com token | 4 |
| `internal/bootstrap/bootstrap_integration_test.go` | caso "só consumidor" no `TestFxFailFast` | 4 |
| `test/testkit/harness.go` | `Harness.ReceiveDLQ`; `OIDCConfig` no fixture | 4, 5 |
| `test/integration/auth_test.go` | A05; `tokenExpiry` → `testkit.TokenExpiry` | 4, 5 |
| `test/testkit/auth.go` | `TokenExpiry`, `OIDCConfig`, `NewVerifier` | 5 |
| `internal/adapters/sqsconsumer/auth_integration_test.go` (novo) | A06 | 5 |
| `test/integration/observability_test.go` | I14 com um token SQS próprio | 6 |
| `docs/…`, `ARCHITECTURE.md`, `README.md`, `deploy/grafana/dashboards/pda.json` | spec §8, "no encerramento" | 7 |

---

### Tarefa 1: o `testkit` anexa o token do provedor (decisão 9)

Mudança inofensiva enquanto o consumidor não lê o atributo. Vem antes para que nenhum teste quebre quando a exigência for ligada (spec §10, risco 2).

**Arquivos:** modificar `test/testkit/sqs.go`, `test/integration/harness_test.go`; criar `test/testkit/sqs_test.go`.

**Interfaces:**
- Produz: `testkit.SendOpts{…, Token string, NoToken bool}`; `testkit.BodyProviderID(body string) string`; `tokenFor(body string, o SendOpts, token func(clientID string) string) string` (não exportada).

- [ ] **Passo 1: escrever o teste** (`test/testkit/sqs_test.go`)

```go
package testkit

import "testing"

// Covers: D-23 (spec of 01/10, decision 9: the token every test message carries)
func TestTokenFor(t *testing.T) {
	tokenOf := func(clientID string) string { return "token-of-" + clientID }
	const bodyB = `{"messageId":"m","type":"WagerTransactionRequested","occurredAt":"2026-10-01T12:00:00Z","data":{"providerId":"provider-b"}}`
	cases := []struct {
		name, body string
		o          SendOpts
		want       string
	}{
		{"the provider named in the body", bodyB, SendOpts{}, "token-of-provider-b"},
		{"provider-a when the body names none", `{"messageId":"m","data":{"walletId":"w"}}`, SendOpts{}, "token-of-provider-a"},
		{"provider-a for a malformed body", `{"messageId":`, SendOpts{}, "token-of-provider-a"},
		{"an explicit token", bodyB, SendOpts{Token: "forged"}, "forged"},
		{"no token", bodyB, SendOpts{NoToken: true}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tokenFor(tc.body, tc.o, tokenOf); got != tc.want {
				t.Fatalf("tokenFor = %q, want %q", got, tc.want)
			}
		})
	}
}
```

- [ ] **Passo 2: stubs para o red.** Em `test/testkit/sqs.go`, acrescentar os campos `Token string` e `NoToken bool` ao `SendOpts` e:

```go
func tokenFor(string, SendOpts, func(string) string) string { return "" }

func BodyProviderID(string) string { return "" }
```

- [ ] **Passo 3: ver falhar.** `go test -race -count=1 -run '^TestTokenFor$' ./test/testkit/`. Esperado: FAIL nos casos "the provider named in the body", "provider-a…" e "an explicit token" (`tokenFor = "", want …`). O caso "no token" passa.

- [ ] **Passo 4: implementar** (`test/testkit/sqs.go`). O `SendOpts` fica assim:

```go
// SendOpts are the send attributes of messaging.md §3.2.
type SendOpts struct {
	GroupID       string
	DedupID       string // "" = a new one: the app's deduplication is exercised, not the FIFO's (TST-C11)
	CorrelationID string // "" = no correlationId attribute
	// Token is the accessToken attribute (D-23). "" = a real token of the
	// provider named in data.providerId, or of provider-a when the body names
	// none (a malformed body, an event of the audit queue).
	Token string
	// NoToken sends without the accessToken attribute.
	NoToken bool
}

// accessTokenAttribute is the message attribute of the provider's token
// (messaging.md §3.2, D-23).
const accessTokenAttribute = "accessToken"

// tokenFor is the accessToken SendMessage attaches (spec of 01/10, decision 9).
func tokenFor(body string, o SendOpts, token func(clientID string) string) string {
	switch {
	case o.NoToken:
		return ""
	case o.Token != "":
		return o.Token
	}
	return token(cmp.Or(BodyProviderID(body), "provider-a"))
}

// BodyProviderID is the data.providerId of a WagerTransactionRequested body,
// "" when the body names none.
func BodyProviderID(body string) string {
	var env struct {
		Data struct {
			ProviderID string `json:"providerId"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(body), &env) // a malformed body names no provider
	return env.Data.ProviderID
}

func stringAttribute(v string) types.MessageAttributeValue {
	return types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(v)}
}
```

E no `SendMessage`, o bloco do `correlationId` é trocado por:

```go
	attrs := map[string]types.MessageAttributeValue{}
	if o.CorrelationID != "" {
		attrs["correlationId"] = stringAttribute(o.CorrelationID)
	}
	if token := tokenFor(body, o, func(clientID string) string { return Token(tb, clientID) }); token != "" {
		attrs[accessTokenAttribute] = stringAttribute(token)
	}
	if len(attrs) > 0 {
		in.MessageAttributes = attrs
	}
```

Acrescentar `"cmp"` aos imports.

- [ ] **Passo 5: ver passar.** `go test -race -count=1 -run '^TestTokenFor$' ./test/testkit/`. Esperado: PASS.

- [ ] **Passo 6: fila de auditoria sem token.** Em `test/integration/harness_test.go`, nas três chamadas `testkit.SendMessage(t, sqsClient, topic.AuditQueueURL, …, testkit.SendOpts{GroupID: wallet, DedupID: id})` (linhas 147, 224 e 249), trocar por `testkit.SendOpts{GroupID: wallet, DedupID: id, NoToken: true}`. A fila de auditoria leva eventos, não mensagens de aposta.

- [ ] **Checkpoint:** `make check` verde e `go vet -tags=integration,e2e,faultinject ./...` sem avisos.

---

### Tarefa 2: `auth`: validade num instante e chaves indisponíveis (decisões 2, 3 e 6; U33, U34)

**Arquivos:** criar `internal/auth/keyset.go`; modificar `internal/auth/verifier.go` e `internal/auth/verifier_test.go`.

**Interfaces:**
- Produz:
  - `var auth.ErrKeysUnavailable error`;
  - `func (v *auth.Verifier) AuthenticateAt(ctx context.Context, raw string, at time.Time) (auth.Principal, error)`;
  - `Authenticate` passa a devolver `ErrKeysUnavailable` (em vez de `ErrUnauthenticated`) quando as chaves não puderam ser buscadas.

- [ ] **Passo 1: `idp.status` atômico** (`verifier_test.go`). O U34 alterna o status enquanto o servidor de teste responde. Trocar o campo `status int` por `status atomic.Int32` e, no handler:

```go
		if s := i.status.Load(); s != 0 {
			w.WriteHeader(int(s))
			return
		}
```

No `TestVerifierCheckKeys`, trocar `i.status = http.StatusServiceUnavailable` por `i.status.Store(http.StatusServiceUnavailable)`. Acrescentar `"sync/atomic"` e `"context"` aos imports.

- [ ] **Passo 2: escrever os testes** (`verifier_test.go`)

```go
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
```

O caso cancelado usa um verificador novo: uma busca abandonada pelo cancelamento continua em andamento no go-oidc, e quem a reutilizasse receberia o resultado dela.

- [ ] **Passo 3: stubs para o red** (`verifier.go`)

```go
// ErrKeysUnavailable reports that the keys of the IdP could not be fetched:
// the token was not judged, so the failure is transient (D-23).
var ErrKeysUnavailable = errors.New("auth: identity provider keys unavailable")

func (v *Verifier) AuthenticateAt(context.Context, string, time.Time) (Principal, error) {
	return Principal{}, ErrUnauthenticated
}
```

- [ ] **Passo 4: ver falhar.** `go test -race -count=1 -run '^(TestAuthenticateAt|TestVerifierKeysUnavailable)$' ./internal/auth/`. Esperado:
  - U33: FAIL em "accepts a token valid at the instant and expired now" e nos dois `accepted` (`AuthenticateAt = …, auth: unauthenticated`);
  - U34: FAIL na primeira asserção (`Authenticate with the JWKS answering 503 = auth: unauthenticated: …, want ErrKeysUnavailable only`).

- [ ] **Passo 5: implementar `keyset.go`** (novo)

```go
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
```

- [ ] **Passo 6: implementar `verifier.go`.** Substituir o stub e reorganizar o `Verifier`:

```go
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
```

O `CheckKeys` não muda.

- [ ] **Passo 7: ver passar.** `go test -race -count=1 ./internal/auth/`. Esperado: PASS (U16, U33, U34 e os demais).

- [ ] **Passo 8: sensibilidade do U34.** No `NewVerifier`, usar o `oidc.NewRemoteKeySet(…)` direto, sem o `observedKeySet`, e confirmar que o U34 falha na primeira asserção. Desfazer. Registrar no teste: `// Sensitivity: NewVerifier with the RemoteKeySet itself, without observedKeySet → "Authenticate with the JWKS answering 503 = auth: unauthenticated: …".`

- [ ] **Checkpoint:** `make check` verde.

---

### Tarefa 3: o `auth.Module` também no papel de consumidor (decisão 7)

**Arquivos:** modificar `internal/bootstrap/bootstrap.go` e `internal/bootstrap/bootstrap_test.go`.

**Interfaces:**
- Consome: `auth.Module`, que fornece o `*auth.Verifier`.
- Produz: o grafo com `CONSUMER_ENABLED` e sem `HTTP_ENABLED` passa a ter o `*auth.Verifier`.

- [ ] **Passo 1: escrever o teste.** No `TestOptionsFor`, antes do subteste "every role off is a valid graph":

```go
	t.Run("the consumer without the HTTP has the verifier (D-23)", func(t *testing.T) {
		needVerifier := fx.Invoke(func(*auth.Verifier) {})
		if err := fx.ValidateApp(append(bootstrap.OptionsFor(config.Roles{Consumer: true}), needVerifier)...); err != nil {
			t.Fatalf("ValidateApp() = %v, want the verifier with the consumer on", err)
		}
		err := fx.ValidateApp(append(bootstrap.OptionsFor(config.Roles{OutboxPublisher: true, ReferenceWorker: true}), needVerifier)...)
		if err == nil || !strings.Contains(err.Error(), "missing type") {
			t.Fatalf("ValidateApp() = %v, want no verifier without the HTTP and the consumer", err)
		}
	})
```

- [ ] **Passo 2: ver falhar.** `go test -race -count=1 -run '^TestOptionsFor$' ./internal/bootstrap/`. Esperado: FAIL com `ValidateApp() = … missing type: *auth.Verifier …, want the verifier with the consumer on`.

- [ ] **Passo 3: implementar** (`bootstrap.go`, em `OptionsFor`):

```go
	if roles.HTTP || roles.Consumer {
		opts = append(opts, auth.Module) // bearer tokens of the API and accessToken of the messages (D-23)
	}
```

O comentário do `OptionsFor` passa a dizer: "o `auth` entra com o HTTP ou com o consumidor".

- [ ] **Passo 4: ver passar.** `go test -race -count=1 ./internal/bootstrap/`. Esperado: PASS.

- [ ] **Checkpoint:** `make check` verde.

---

### Tarefa 4: o consumidor exige a identidade do provedor (decisões 1, 4, 5 e 8; A05, U35)

**Arquivos:**
- criar `internal/adapters/sqsconsumer/authorize.go` e `authorize_test.go`;
- modificar `consumer.go`, `handler.go` e `module.go`;
- modificar os testes `consumer_test.go`, `handler_test.go` e `helpers_integration_test.go`;
- modificar `internal/bootstrap/bootstrap_integration_test.go`, `test/testkit/harness.go`, `test/testkit/sqs.go` e `test/integration/auth_test.go`.

**Interfaces:**
- Consome: `auth.ErrKeysUnavailable`, `auth.HasRole`, `auth.ActsAs`, `auth.RoleProvider`; `(*auth.Verifier).AuthenticateAt` (Tarefa 2); `testkit.SendOpts{Token, NoToken}` e `testkit.BodyProviderID` (Tarefa 1).
- Produz:
  - `const sqsconsumer.AccessTokenAttribute = "accessToken"`;
  - `type sqsconsumer.Authenticator interface { AuthenticateAt(ctx context.Context, raw string, at time.Time) (auth.Principal, error) }`;
  - `func sqsconsumer.NewConsumer(api QueueAPI, queues *awsclient.Queues, proc Processor, authn Authenticator, db Pinger, m Metrics, log *slog.Logger, opts Options) *Consumer`;
  - `Metrics` ganha `AuthFailure(reason string)`;
  - `func (h *testkit.Harness) ReceiveDLQ(tb testing.TB, n int) []testkit.DLQMessage`.

- [ ] **Passo 1: escrever o A05** (`test/integration/auth_test.go`) e o helper do `Harness` (`test/testkit/harness.go`)

```go
// ReceiveDLQ reads and deletes n messages of the DLQ (testkit.ReceiveDLQ).
func (h *Harness) ReceiveDLQ(tb testing.TB, n int) []DLQMessage {
	tb.Helper()
	return ReceiveDLQ(tb, h.sqs, h.DLQURL, n)
}
```

```go
// Covers: AUTH-04, AUTH-05, AUTH-07, AUTH-09, TST-A02, TST-A03, E2 (A05)
//
// The scenario of the final audit (D-23): provider-b, with its own token,
// sends a REFUND naming provider-a against a BET of provider-a. It, and the
// messages without a valid provider token, go to the DLQ and record nothing.
// The queue is the package's isolated one; the IAM permission to send is I04f.
//
// Not parallel: the counts are global, so no other test may write meanwhile.
func TestSQSProviderIdentity(t *testing.T) {
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet := wager(w, "provider-a", "BET", "30.00", unique("bet"), "")
	result(t, server.Client(t, "provider-a"), bet, http.StatusOK)
	before := testkit.SnapshotCounts(t, server.Owner())

	sends := []struct {
		code string
		body testkit.Wager
		opts testkit.SendOpts
	}{
		{"PROVIDER_MISMATCH", wager(w, "provider-a", "REFUND", "30.00", unique("refund"), bet.ExternalTransactionID),
			testkit.SendOpts{Token: testkit.Token(t, "provider-b")}},
		{"UNAUTHENTICATED", wager(w, "provider-a", "BET", "1.00", unique("bet"), ""), testkit.SendOpts{NoToken: true}},
		{"FORBIDDEN", wager(w, "provider-a", "BET", "1.00", unique("bet"), ""), testkit.SendOpts{Token: testkit.Token(t, "wallet-service")}},
		{"UNAUTHENTICATED", wager(w, "provider-a", "BET", "1.00", unique("bet"), ""), testkit.SendOpts{Token: testkit.ForgedToken(t)}},
	}
	want := map[string]string{} // SQS id → errorCode
	for _, s := range sends {
		o := s.opts
		o.GroupID = w.ID
		want[server.SendWager(t, sqsWager(t, unique("msg"), s.body), o)] = s.code
	}
	for _, m := range server.ReceiveDLQ(t, len(sends)) {
		id := m.Attributes["originalMessageId"]
		if code, ok := want[id]; !ok || m.Attributes["errorCode"] != code || m.Attributes["errorCategory"] != "CORRECTABLE" {
			t.Errorf("DLQ message of %s = %v, want errorCode %s", id, m.Attributes, code)
		}
		if _, leaked := m.Attributes["accessToken"]; leaked {
			t.Errorf("the DLQ copy of %s carries the access token", id)
		}
	}
	server.AssertQueueDrained(t)
	if after := testkit.SnapshotCounts(t, server.Owner()); !maps.Equal(before, after) {
		t.Fatalf("rows changed: before %v, after %v", before, after)
	}
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("70.00") || got.Version != 2 {
		t.Fatalf("wallet = %+v, want 70.00 after the BET only", got)
	}
}
```

- [ ] **Passo 2: ver o A05 falhar** (a reprodução da auditoria). `make infra-up && go test -tags=integration -race -count=1 -run '^TestSQSProviderIdentity$' ./test/integration/`. Esperado: FAIL depois de ~20 s em `ReceiveDLQ` ("4 messages in the DLQ" não acontece). As 4 mensagens são processadas, inclusive o REFUND do `provider-b` em nome do `provider-a`.

- [ ] **Passo 3: escrever o caso "só consumidor" do `TestFxFailFast`** (`internal/bootstrap/bootstrap_integration_test.go`, depois do laço de `cases`):

```go
	t.Run("unreachable identity provider, consumer only (D-23)", func(t *testing.T) {
		cfg := integrationConfig(t)
		cfg.OIDCJWKSURL = "http://127.0.0.1:1/certs"
		app := fx.New(append(bootstrap.OptionsFor(config.Roles{Consumer: true}), fx.Replace(cfg), fx.NopLogger)...)
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		err := app.Start(ctx)
		if err == nil {
			_ = app.Stop(ctx)
			t.Fatal("Start() error = nil, want the JWKS fail-fast of the consumer")
		}
		if !strings.Contains(err.Error(), "JWKS") {
			t.Fatalf("Start() error = %q, want it to mention JWKS", err.Error())
		}
	})
```

- [ ] **Passo 4: ver falhar.** `go test -tags=integration -race -count=1 -run '^TestFxFailFast$' ./internal/bootstrap/`. Esperado: FAIL com `Start() error = nil, want the JWKS fail-fast of the consumer`. O Fx só constrói o verificador quando alguém depende dele, e o consumidor ainda não depende.

- [ ] **Passo 5: escrever a linha nova do `TestDecide`** (`handler_test.go`, depois de "conflict"):

```go
		{
			"refused authorization (D-23)",
			conclusion{err: apperrors.New(apperrors.KindForbidden, "PROVIDER_MISMATCH", errors.New("x"))},
			false,
			action{kind: actDLQ, code: "PROVIDER_MISMATCH", category: "CORRECTABLE"},
		},
```

- [ ] **Passo 6: ver falhar.** `go test -race -count=1 -run '^TestDecide$' ./internal/adapters/sqsconsumer/`. Esperado: FAIL em "refused authorization" (`decide = {kind:2 … transient:true}`, um retry, `want {kind:1 code:PROVIDER_MISMATCH …}`).

- [ ] **Passo 7: escrever o U35** (`authorize_test.go`, novo)

```go
package sqsconsumer

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/auth"
)

// stubAuth answers AuthenticateAt with p and err and records the calls.
type stubAuth struct {
	p     auth.Principal
	err   error
	calls int
	at    time.Time
}

func (s *stubAuth) AuthenticateAt(_ context.Context, _ string, at time.Time) (auth.Principal, error) {
	s.calls++
	s.at = at
	return s.p, s.err
}

// recordingProcessor counts the messages that reach the use case.
type recordingProcessor struct{ calls atomic.Int32 }

func (r *recordingProcessor) Execute(context.Context, app.WagerMessage) (app.ConsumeResult, error) {
	r.calls.Add(1)
	return app.ConsumeResult{Duplicate: true}, nil
}

// Covers: AUTH-04, AUTH-07, AUTH-09, SQS-07, D-23 (U35)
func TestAuthorizeMessage(t *testing.T) {
	sent := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	received := sent.Add(10 * time.Minute)
	providerA := auth.Principal{ProviderID: "provider-a", Roles: []auth.Role{auth.RoleProvider}}
	providerB := auth.Principal{ProviderID: "provider-b", Roles: []auth.Role{auth.RoleProvider}}
	internal := auth.Principal{ClientID: "wallet-service", Roles: []auth.Role{auth.RoleWalletInternal}}
	unnamed := auth.Principal{Roles: []auth.Role{auth.RoleProvider}} // the provider role without provider_id

	withoutProvider := strings.Replace(validBody, `"providerId":"provider-a",`, "", 1)
	emptyProvider := strings.Replace(validBody, `"providerId":"provider-a"`, `"providerId":""`, 1)
	invalidAmount := strings.Replace(validBody, `"amount":"25.00"`, `"amount":"1"`, 1)
	token, empty := "a-token", ""

	type want struct {
		kind           actionKind
		code, category string
		processed      bool   // the use case was called
		reason         string // auth_failures_total, "" = none
	}
	refused := func(code string) want {
		return want{kind: actDLQ, code: code, category: "CORRECTABLE", reason: strings.ToLower(code)}
	}
	concluded := want{kind: actDelete, processed: true}
	cases := []struct {
		name  string
		body  string
		token *string // nil = no accessToken attribute
		auth  *stubAuth
		want  want
	}{
		{"no accessToken", validBody, nil, &stubAuth{p: providerA}, refused("UNAUTHENTICATED")},
		{"an empty accessToken", validBody, &empty, &stubAuth{p: providerA}, refused("UNAUTHENTICATED")},
		{"a token the IdP refuses", validBody, &token, &stubAuth{err: fmt.Errorf("%w: forged", auth.ErrUnauthenticated)}, refused("UNAUTHENTICATED")},
		{"the IdP keys unavailable", validBody, &token, &stubAuth{err: fmt.Errorf("%w: 503", auth.ErrKeysUnavailable)}, want{kind: actRetry}},
		{"the internal service", validBody, &token, &stubAuth{p: internal}, refused("FORBIDDEN")},
		{"a provider without provider_id", validBody, &token, &stubAuth{p: unnamed}, refused("FORBIDDEN")},
		{"another provider", validBody, &token, &stubAuth{p: providerB}, refused("PROVIDER_MISMATCH")},
		{"an empty providerId", emptyProvider, &token, &stubAuth{p: providerA}, refused("PROVIDER_MISMATCH")},
		{"another provider and an invalid amount", invalidAmount, &token, &stubAuth{p: providerB}, refused("PROVIDER_MISMATCH")},
		{"no providerId: the use case decides", withoutProvider, &token, &stubAuth{p: providerA}, concluded},
		{"the provider of the token", validBody, &token, &stubAuth{p: providerA}, concluded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := types.Message{
				MessageId: aws.String("sqs-1"), ReceiptHandle: aws.String("rh-1"), Body: aws.String(tc.body),
				Attributes: map[string]string{"MessageGroupId": "g", "SentTimestamp": strconv.FormatInt(sent.UnixMilli(), 10)},
			}
			if tc.token != nil {
				msg.MessageAttributes = map[string]types.MessageAttributeValue{
					AccessTokenAttribute: {DataType: aws.String("String"), StringValue: aws.String(*tc.token)},
				}
			}
			m, proc := &countingMetrics{}, &recordingProcessor{}
			c := NewConsumer(&fakeQueue{}, &awsclient.Queues{WagerURL: "wager", DLQURL: "dlq"}, proc, tc.auth,
				upPinger{}, m, slog.New(slog.DiscardHandler), unitOptions(0))
			a := c.handle(t.Context(), msg, received)
			if a.kind != tc.want.kind || a.code != tc.want.code || a.category != tc.want.category {
				t.Fatalf("action = %+v, want %+v", a, tc.want)
			}
			if got := proc.calls.Load() > 0; got != tc.want.processed {
				t.Fatalf("use case called = %v, want %v", got, tc.want.processed)
			}
			var wantReasons []string
			if tc.want.reason != "" {
				wantReasons = []string{tc.want.reason}
			}
			if got := m.reasons(); !slices.Equal(got, wantReasons) {
				t.Fatalf("auth failures = %v, want %v", got, wantReasons)
			}
			if tc.token == nil || *tc.token == "" {
				if tc.auth.calls != 0 {
					t.Fatalf("the IdP was asked %d times about a message without a token", tc.auth.calls)
				}
			}
		})
	}

	instants := map[string]struct {
		sentTimestamp string // "" = no SentTimestamp
		want          time.Time
	}{
		"the SentTimestamp of the broker":               {strconv.FormatInt(sent.UnixMilli(), 10), sent},
		"no SentTimestamp: the receive time":            {"", received},
		"an unreadable SentTimestamp: the receive time": {"yesterday", received},
	}
	for name, tc := range instants {
		t.Run("the instant of the token is "+name, func(t *testing.T) {
			msg := types.Message{
				MessageId: aws.String("sqs-1"), ReceiptHandle: aws.String("rh-1"), Body: aws.String(validBody),
				Attributes: map[string]string{"MessageGroupId": "g"},
				MessageAttributes: map[string]types.MessageAttributeValue{
					AccessTokenAttribute: {DataType: aws.String("String"), StringValue: aws.String(token)},
				},
			}
			if tc.sentTimestamp != "" {
				msg.Attributes["SentTimestamp"] = tc.sentTimestamp
			}
			a := &stubAuth{p: providerA}
			c := NewConsumer(&fakeQueue{}, &awsclient.Queues{WagerURL: "wager", DLQURL: "dlq"}, &recordingProcessor{}, a,
				upPinger{}, &countingMetrics{}, slog.New(slog.DiscardHandler), unitOptions(0))
			c.handle(t.Context(), msg, received)
			if !a.at.Equal(tc.want) {
				t.Fatalf("the token was checked at %s, want %s", a.at, tc.want)
			}
		})
	}
}
```

- [ ] **Passo 8: stubs, assinatura e adaptações para compilar.**

`authorize.go` (novo), primeiro com stubs:

```go
package sqsconsumer

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/auth"
)

// AccessTokenAttribute is the message attribute with the provider's access
// token (messaging.md §3.2, D-23).
const AccessTokenAttribute = "accessToken"

// Authenticator verifies an access token at an instant: auth.Verifier (D-23).
type Authenticator interface {
	AuthenticateAt(ctx context.Context, raw string, at time.Time) (auth.Principal, error)
}

func authenticate(context.Context, Authenticator, types.Message, time.Time) (auth.Principal, error) {
	return auth.Principal{}, nil
}

func matchProvider(auth.Principal, app.WagerMessage) error { return nil }

func (c *Consumer) countRefusal(error) {}
```

`consumer.go`:
- `Metrics` ganha `AuthFailure(reason string)`;
- o `Consumer` ganha o campo `authn Authenticator`;
- o `NewConsumer` passa a `NewConsumer(api QueueAPI, queues *awsclient.Queues, proc Processor, authn Authenticator, db Pinger, m Metrics, log *slog.Logger, opts Options)` e guarda `authn`;
- o `handle` fica:

```go
// handle concludes one message and applies the action, in the order of D-23:
// the provider's token, the envelope, the provider of the body, then the use
// case. Nothing is read before the authorization.
func (c *Consumer) handle(work context.Context, msg types.Message, receivedAt time.Time) action {
	start := time.Now()
	log := c.log.With("sqsMessageId", aws.ToString(msg.MessageId))
	ctx, cancel := context.WithTimeout(work, c.opts.ProcessingTimeout)
	defer cancel()
	var (
		m   app.WagerMessage
		res app.ConsumeResult
	)
	p, err := authenticate(ctx, c.authn, msg, receivedAt)
	if err == nil {
		m, err = parseEnvelope(aws.ToString(msg.Body), aws.ToString(msg.MessageAttributes[correlationAttribute].StringValue), receivedAt)
	}
	if err == nil {
		log = log.With(messageIDs(m)...)
		err = matchProvider(p, m)
	}
	if err == nil {
		res, err = c.proc.Execute(ctx, m)
	}
	c.countRefusal(err)
	a := decide(conclude(res, err), c.stopping.Load(), receiveCount(msg), c.opts.RetryMaxDelay)
	c.apply(work, msg, a, err, log)
	if a.outcome != "" {
		c.metrics.Processed(a.outcome, time.Since(start))
	}
	if a.duplicate != "" {
		c.metrics.Duplicate(a.duplicate)
	}
	return a
}
```

`module.go`: o `newModuleConsumer` ganha o parâmetro `verifier *auth.Verifier` (import `internal/auth`) e chama `NewConsumer(api, queues, proc, verifier, pool, m, log, Options{…})`.

`consumer_test.go`:

```go
// trustingAuth reads the token as the provider id: these tests exercise the
// queue handling; U35 exercises the authorization.
type trustingAuth struct{}

func (trustingAuth) AuthenticateAt(_ context.Context, raw string, _ time.Time) (auth.Principal, error) {
	return auth.Principal{ProviderID: raw, Roles: []auth.Role{auth.RoleProvider}}, nil
}

// message is a received message of body, with provider-a's token for trustingAuth.
func message(body string) types.Message {
	return types.Message{
		MessageId: aws.String("sqs-1"), ReceiptHandle: aws.String("rh-1"), Body: aws.String(body),
		Attributes: map[string]string{"MessageGroupId": "g"},
		MessageAttributes: map[string]types.MessageAttributeValue{
			AccessTokenAttribute: {DataType: aws.String("String"), StringValue: aws.String("provider-a")},
		},
	}
}

// unitOptions are the consumer settings of the unit tests.
func unitOptions(waitTime time.Duration) Options {
	return Options{
		Pollers: 1, ReceiveBatch: 10, WaitTime: waitTime, Visibility: 5 * time.Second,
		ProcessingTimeout: 3 * time.Second, MaxInFlight: 4, RetryMaxDelay: time.Second,
		ShutdownTimeout: time.Second, DLQName: "dlq",
	}
}
```

- `unitConsumerWith` chama `NewConsumer(q, &awsclient.Queues{WagerURL: "wager", DLQURL: "dlq"}, proc, trustingAuth{}, upPinger{}, m, log, unitOptions(waitTime))`;
- o `TestDeleteFailureIsCounted` e o `TestFailureLogsCarryTheMessageIDs` montam a mensagem com `message(validBody)` e `message(tc.body)`, no lugar do `types.Message{…}` literal;
- o `countingMetrics` ganha:

```go
	mu          sync.Mutex
	authReasons []string
```

```go
func (m *countingMetrics) AuthFailure(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.authReasons = append(m.authReasons, reason)
}

func (m *countingMetrics) reasons() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.authReasons...)
}
```

`helpers_integration_test.go`:

```go
// consumerOpts customizes one consumer of a fixture.
type consumerOpts struct {
	proc   sqsconsumer.Processor     // nil = the real use case
	api    sqsconsumer.QueueAPI      // nil = the SQS client
	pinger sqsconsumer.Pinger        // nil = the package database
	auth   sqsconsumer.Authenticator // nil = trustingAuth
	opts   *sqsconsumer.Options      // nil = options()
}

// trustingAuth reads the token as the provider id. The tests of this package
// use a provider per test ("provider-<id>"), which the IdP does not know; the
// real IdP is exercised here by A06 and by every SQS test of test/integration.
type trustingAuth struct{}

func (trustingAuth) AuthenticateAt(_ context.Context, raw string, _ time.Time) (auth.Principal, error) {
	return auth.Principal{ProviderID: raw, Roles: []auth.Role{auth.RoleProvider}}, nil
}
```

- no `start`: `if o.auth == nil { o.auth = trustingAuth{} }` e `sqsconsumer.NewConsumer(o.api, &f.queues, o.proc, o.auth, o.pinger, …)`;
- o `send` fica:

```go
func (f *fixture) send(t *testing.T, body, group string) string {
	t.Helper()
	token := cmp.Or(testkit.BodyProviderID(body), "provider-a") // read as the provider id by trustingAuth
	return testkit.SendMessage(t, f.sqs, f.queues.WagerURL, body, testkit.SendOpts{GroupID: group, Token: token})
}
```

- imports novos: `"cmp"` e `internal/auth`.

- [ ] **Passo 9: ver o U35 falhar.** `go test -race -count=1 -run '^TestAuthorizeMessage$' ./internal/adapters/sqsconsumer/`. Esperado: FAIL nos casos de recusa (`action = {kind:0 …}`, um delete, `want {kind:1 code:UNAUTHENTICATED …}`) e nos de instante (`the token was checked at 0001-01-01…`). Os dois casos `concluded` passam.

- [ ] **Passo 10: implementar.**

`authorize.go`, trocando os stubs (imports finais: `context`, `errors`, `fmt`, `strconv`, `strings`, `time`, `aws`, `types`, `app`, `apperrors`, `auth`):

```go
// Codes of the refused messages: those of the HTTP edge (lifecycle §5.3).
const (
	codeUnauthenticated  = "UNAUTHENTICATED"
	codeForbidden        = "FORBIDDEN"
	codeProviderMismatch = "PROVIDER_MISMATCH"
)

// authenticate verifies the accessToken of msg at its SentTimestamp (the
// receive instant when the broker gave none) and requires the provider role.
// A refusal is KindForbidden with its code; keys the IdP could not serve are
// KindTransient (D-23).
func authenticate(ctx context.Context, a Authenticator, msg types.Message, receivedAt time.Time) (auth.Principal, error) {
	raw := aws.ToString(msg.MessageAttributes[AccessTokenAttribute].StringValue)
	if raw == "" {
		return auth.Principal{}, refused(codeUnauthenticated, errors.New("no access token"))
	}
	p, err := a.AuthenticateAt(ctx, raw, sentAt(msg, receivedAt))
	switch {
	case errors.Is(err, auth.ErrKeysUnavailable):
		return auth.Principal{}, apperrors.New(apperrors.KindTransient, "", err)
	case err != nil:
		return auth.Principal{}, refused(codeUnauthenticated, err)
	case !auth.HasRole(p, auth.RoleProvider):
		return auth.Principal{}, refused(codeForbidden, errors.New("the token has no provider role"))
	}
	return p, nil
}

// matchProvider requires data.providerId, whenever the message carries it
// (even empty), to be the provider of the token. An absent one is left to the
// validation of the use case (MISSING_FIELD).
func matchProvider(p auth.Principal, m app.WagerMessage) error {
	if id := m.Input.ProviderID; id != nil && !auth.ActsAs(p, *id) {
		return refused(codeProviderMismatch, errors.New("data.providerId is not the provider of the token"))
	}
	return nil
}

// sentAt is the SentTimestamp of msg (epoch milliseconds), or receivedAt.
func sentAt(msg types.Message, receivedAt time.Time) time.Time {
	ms, err := strconv.ParseInt(msg.Attributes[string(types.MessageSystemAttributeNameSentTimestamp)], 10, 64)
	if err != nil {
		return receivedAt
	}
	return time.UnixMilli(ms)
}

func refused(code string, err error) error {
	return apperrors.New(apperrors.KindForbidden, code, fmt.Errorf("sqsconsumer: %w", err))
}

// countRefusal reports a message refused by the authorization to
// auth_failures_total, with the reasons of the HTTP edge (D-23).
func (c *Consumer) countRefusal(err error) {
	if apperrors.Classify(err) == apperrors.KindForbidden {
		c.metrics.AuthFailure(strings.ToLower(apperrors.CodeOf(err)))
	}
}
```

`handler.go`, no `decide`:

```go
		case apperrors.KindInput, apperrors.KindConflict, apperrors.KindForbidden:
```

(o corpo do ramo não muda: DLQ `CORRECTABLE` com o código). O ramo de retry fica `case apperrors.KindTransient, apperrors.KindNotFound, apperrors.KindBusiness:`, com o comentário `// Retried below; the use case returns neither of the last two.`

`consumer.go`, no `ReceiveMessage` do `pollLoop`:

```go
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{
				types.MessageSystemAttributeNameMessageGroupId, types.MessageSystemAttributeNameApproximateReceiveCount,
				types.MessageSystemAttributeNameSentTimestamp,
			},
			MessageAttributeNames: []string{correlationAttribute, AccessTokenAttribute},
```

- [ ] **Passo 11: ver os unitários passarem.** `go test -race -count=1 ./internal/adapters/sqsconsumer/`. Esperado: PASS (U35, `TestDecide`, U25, `TestDeleteFailureIsCounted` e os demais).

- [ ] **Passo 12: o token fora da cópia da DLQ** (Foco da revisão 3; comportamento existente). No `TestDLQInput`, logo depois de montar o `msg`:

```go
	msg.MessageAttributes = map[string]types.MessageAttributeValue{
		AccessTokenAttribute: {DataType: aws.String("String"), StringValue: aws.String("a-provider-token")},
		"correlationId":      {DataType: aws.String("String"), StringValue: aws.String("corr-1")},
	}
```

e, depois da conferência dos atributos:

```go
	if _, ok := in.MessageAttributes[AccessTokenAttribute]; ok {
		t.Fatal("the DLQ copy carries the access token (D-23)")
	}
```

Rodar `go test -race -count=1 -run '^TestDLQInput$' ./internal/adapters/sqsconsumer/` (PASS). **Sensibilidade:** no `dlqInput`, copiar os `msg.MessageAttributes` para o input; o teste falha; desfazer. Registrar: `// Sensitivity: dlqInput copying msg.MessageAttributes → "the DLQ copy carries the access token".`

- [ ] **Passo 13: integração verde.** `make infra-up`, depois:
  - `go test -tags=integration -race -count=1 ./internal/adapters/sqsconsumer/ ./internal/bootstrap/`
  - `go test -tags=integration -race -count=1 -run '^TestSQSProviderIdentity$' ./test/integration/`

  Esperado: PASS, inclusive o caso novo do `TestFxFailFast` e o A05.

- [ ] **Passo 14: sensibilidade do A05.** Fazer o `matchProvider` devolver `nil` sempre. O A05 falha: o REFUND do `provider-b` é processado, e `ReceiveDLQ` recebe 3 de 4. Desfazer. Registrar no A05: `// Sensitivity: matchProvider returning nil → the REFUND of provider-b in the name of provider-a is PROCESSED and the DLQ gets 3 of 4 messages.`

- [ ] **Passo 15: refactor.** Em `test/testkit/sqs.go`, apagar a constante `accessTokenAttribute` e usar `sqsconsumer.AccessTokenAttribute` (import `internal/adapters/sqsconsumer`). Rodar `go test -race -count=1 ./test/testkit/` (PASS).

- [ ] **Checkpoint:** `make check` e `make test-integration` verdes.

---

### Tarefa 5: o token vale no instante do envio, com o IdP real (decisão 3; A06)

O comportamento já existe (Tarefas 2 e 4), então este teste passa de primeira e precisa de checagem de sensibilidade.

**Arquivos:**
- modificar `test/testkit/auth.go` e `test/testkit/harness.go`;
- criar `internal/adapters/sqsconsumer/auth_integration_test.go`;
- modificar `test/integration/auth_test.go`.

**Interfaces:**
- Produz:
  - `func testkit.TokenExpiry(tb testing.TB, raw string) time.Time`;
  - `func testkit.OIDCConfig(cfg config.Config) config.Config`;
  - `func testkit.NewVerifier(tb testing.TB) *auth.Verifier`.

- [ ] **Passo 1: helpers do `testkit`** (`auth.go`). O `tokenExpiry` sai de `test/integration/auth_test.go`:

```go
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
```

- No `harness.go` (`newFixture`), as duas linhas de OIDC viram `cfg = OIDCConfig(cfg)`.
- Em `test/integration/auth_test.go`, apagar a função `tokenExpiry` e trocar o uso no `expiredToken` por `testkit.TokenExpiry(t, raw)`. Remover os imports que ficarem sem uso (`encoding/base64`, `encoding/json`).
- Rodar `go vet -tags=integration ./test/... ./internal/...`.

- [ ] **Passo 2: escrever o A06** (`auth_integration_test.go`, novo)

```go
//go:build integration

package sqsconsumer_test

import (
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: AUTH-09, SQS-07, D-23 (A06)
//
// The token is checked at the SentTimestamp, with the real IdP: a message sent
// while its token was valid is processed after the token expired; a message
// sent with the expired token goes to the DLQ.
func TestTokenCheckedAtSendTime(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := openWallet(t, "100.00")
	token := testkit.FreshToken(t, "provider-short-lived") // 5 s of life, provider_id provider-a
	ext := "bet-" + testkit.NewID()
	testkit.SendMessage(t, f.sqs, f.queues.WagerURL, wager(t, "msg-"+testkit.NewID(), w, "provider-a", "BET", "1.00", ext, ""),
		testkit.SendOpts{GroupID: w.id, Token: token})
	// Past the exp and the 1 s skew of testkit.NewVerifier: the token is expired from here on.
	time.Sleep(time.Until(testkit.TokenExpiry(t, token).Add(1500 * time.Millisecond)))
	late := testkit.SendMessage(t, f.sqs, f.queues.WagerURL,
		wager(t, "msg-"+testkit.NewID(), w, "provider-a", "BET", "2.00", "bet-"+testkit.NewID(), ""),
		testkit.SendOpts{GroupID: w.id, Token: token})

	f.start(t, consumerOpts{auth: testkit.NewVerifier(t)})
	dlq := testkit.ReceiveDLQ(t, f.sqs, f.queues.DLQURL, 1)
	if dlq[0].Attributes["originalMessageId"] != late || dlq[0].Attributes["errorCode"] != "UNAUTHENTICATED" {
		t.Fatalf("DLQ = %+v, want the message sent with the expired token, UNAUTHENTICATED", dlq[0])
	}
	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	if n := count(t, `SELECT count(*) FROM wager_transactions
		WHERE provider_id = 'provider-a' AND external_transaction_id = $1 AND status = 'PROCESSED'`, ext); n != 1 {
		t.Fatalf("%d PROCESSED for the message sent with a valid token, want 1", n)
	}
	if got := balance(t, w.id); got != "99.00" {
		t.Fatalf("balance = %s, want 99.00", got)
	}
}
```

- [ ] **Passo 3: ver passar.** `make infra-up && go test -tags=integration -race -count=1 -run '^TestTokenCheckedAtSendTime$' ./internal/adapters/sqsconsumer/`. Esperado: PASS em ~7 s.

- [ ] **Passo 4: sensibilidade.** No `authenticate`, passar `receivedAt` no lugar de `sentAt(msg, receivedAt)`. O A06 falha: a primeira mensagem vai para a DLQ, e o `originalMessageId` não é o `late`. Desfazer. Registrar: `// Sensitivity: authenticate passing receivedAt instead of the SentTimestamp → the message sent while its token was valid goes to the DLQ as UNAUTHENTICATED.`

- [ ] **Passo 5: o pacote inteiro.** `go test -tags=integration -race -count=1 ./internal/adapters/sqsconsumer/ ./test/integration/`. Esperado: PASS. O `TestAuthRejects` ("expired") usa o `testkit.TokenExpiry`.

- [ ] **Checkpoint:** `make check` verde.

---

### Tarefa 6: o token do SQS nunca vai para o log (decisão 8; I14)

Comportamento existente: checagem de sensibilidade.

**Arquivos:** modificar `test/integration/observability_test.go`.

- [ ] **Passo 1: estender o I14.** No `TestLogsHaveIdsWithoutSecrets`:

```go
	sqsToken := testkit.FreshToken(t, "provider-a") // a marker of its own: the HTTP token is another
	server.SendWager(t, sqsWager(t, msgID, wager(w, provider, "BET", "12.34", sqsExt, "")),
		testkit.SendOpts{GroupID: w.ID, CorrelationID: corr, Token: sqsToken})
```

(no lugar do envio atual) e `for _, secret := range []string{token, sqsToken, "Bearer ", amount, key} {`.

- [ ] **Passo 2: ver passar.** `make infra-up && go test -tags=integration -race -count=1 -run '^TestLogsHaveIdsWithoutSecrets$' ./test/integration/`. Esperado: PASS.

- [ ] **Passo 3: sensibilidade.** Acrescentar no `handle`, depois do `authenticate`, um `log.InfoContext(ctx, "sqs message authenticated", "accessToken", aws.ToString(msg.MessageAttributes[AccessTokenAttribute].StringValue))`. O I14 falha com `the log leaks "eyJ…"`. Desfazer. Acrescentar ao comentário `// Sensitivity:` do teste: `logging the accessToken in the consumer → the sqsToken check fails.`

- [ ] **Checkpoint:** `make check` verde.

---

### Tarefa 7: regressão completa e documentos do encerramento (spec §7 e §8)

**Arquivos:** os da lista "No encerramento" da spec §8.

- [ ] **Passo 1: regressão.** Rodar `make check`, `make test-integration` e `make test-e2e`, um depois do outro, nunca juntos. Esperado: os três com saída 0. No e2e, os testes que sobem uma instância só com o consumidor (C05a/b) passam a buscar o JWKS no start.

- [ ] **Passo 2: `ARCHITECTURE.md`.**
  - **§9.1:** linha "Identidade do provedor (D-23)": o token no atributo `accessToken`, avaliado no `SentTimestamp`; recusas na DLQ com `UNAUTHENTICATED`, `FORBIDDEN` ou `PROVIDER_MISMATCH`, sem nada gravado; IdP indisponível é transitório.
  - **§10.3**, item "Mensageria": o broker decide quem pode enviar, e o token do IdP diz quem é o provedor; o `data.providerId` precisa ser o `provider_id` do token.
  - **§15**, item 16: "Mensagens SQS carregam o token do provedor (atributo `accessToken`), validado como no HTTP, no instante do envio (D-23)."
  - **§16**, item 2: substituir pela limitação "a DLQ por redrive guarda a mensagem original, com o token (vale ≤ 5 min)" e acrescentar:
    - uma queda do IdP maior que o orçamento de ~18 min leva à DLQ pela redrive (reprocessar pelo reenvio);
    - o `messageId` é global entre provedores (`MESSAGE_HASH_MISMATCH`, sem efeito);
    - com o IdP fora, o HTTP responde 401, não 503.
  - **§17:** no estado da entrega, "a identidade do provedor no SQS (D-23) foi fechada depois da auditoria final de 01/10".

- [ ] **Passo 3: `README.md`.**
  - **§5**, reprocessamento: o reenvio leva um token novo; a cópia da DLQ não traz o token.
  - **§8.8:** o envio passa a incluir `--message-attributes "{\"accessToken\":{\"DataType\":\"String\",\"StringValue\":\"$PROVIDER_TOKEN\"}}"`.
  - **§8.7:** acrescentar o exemplo negado pelo SQS: o `provider-b` com o próprio token nomeando o `provider-a` vai para a DLQ com `PROVIDER_MISMATCH`.
- [ ] **Passo 4: `docs/getting-started.md`**, passo 19: o mesmo atributo no `send-message`, com uma frase sobre por que o token vai junto.

- [ ] **Passo 5: `docs/test-plan.md`:**
  - U33–U35 na tabela de unitários;
  - A05 e A06 na tabela de autenticação;
  - I14 com o token do SQS; I07c com o caso "só consumidor".

- [ ] **Passo 6: `docs/delivery-requirements.md`.** Acrescentar `(01/10, D-23: …)` com os testes em:
  - AUTH-04 e AUTH-05 (A05);
  - AUTH-07 (A05, U35);
  - AUTH-09, reescrito (A05, A06, I04f);
  - TST-A02 e TST-A03 (A05);
  - OBS-02 (I14);
  - FX-01 e FX-02 (`TestOptionsFor`, `TestFxFailFast`).

  Na tabela §0, a linha E2 cita o A05.

- [ ] **Passo 7: `docs/structure.md`:**
  - `auth/keyset.go` e `sqsconsumer/authorize.go` na árvore;
  - no parágrafo dos papéis, "o `auth` entra com o `httpapi` ou com o `sqsconsumer`".

- [ ] **Passo 8: dashboard.** Em `deploy/grafana/dashboards/pda.json`, a `description` do painel de `auth_failures_total` passa a dizer que conta o HTTP (401/403) e as mensagens SQS recusadas. Validação da exceção §4.4: `jq empty deploy/grafana/dashboards/pda.json` e `docker compose up --build --wait` saudável (Tarefa 8).

- [ ] **Passo 9: registros.**
  - `docs/implementation-plan.md`: linha na tabela de riscos, "`providerId` do SQS sem identidade", marcada ✅ (01/10, D-23), com o link da spec.
  - `docs/dev/diary.md`: entrada "01/10/2026 (qui): identidade do provedor no SQS (D-23)" antes de "Onde paramos", com o achado, a sondagem do `SenderId`, as decisões e a evidência.
  - Spec: o status passa a "aprovada pelo autor em 01/10/2026".

- [ ] **Passo 10: links.** Conferir que todo link relativo dos arquivos alterados aponta para um arquivo existente, com o mesmo laço `grep -o '](…)'` usado na revisão da spec.

---

### Tarefa 8: verificação no compose e proposta de commits

- [ ] **Passo 1: compose saudável.** `docker compose up --build --wait`. Esperado: saída 0, os 8 serviços saudáveis.

- [ ] **Passo 2: o cenário da auditoria** (pronto, item 3). Com as funções `aws_as` e `aws_root` do README §5 e §8.8:

```sh
PT=$(scripts/get-token.sh provider-a); PB=$(scripts/get-token.sh provider-b); IT=$(scripts/get-token.sh wallet-service)
PLAYER=$(uuidgen | tr 'A-Z' 'a-z'); R=$(date +%s); QUEUE=http://ministack:4566/000000000000/wager-transactions.fifo
WALLET=$(curl -s -X POST localhost:8081/wallets -H "Authorization: Bearer $IT" -H 'Content-Type: application/json' \
  -d "{\"playerId\":\"$PLAYER\",\"initialBalance\":{\"amount\":\"100.00\",\"currency\":\"BRL\"}}" | jq -r .id)
curl -s -X POST localhost:8082/wagering/transactions -H "Authorization: Bearer $PT" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: provider-a:bet-$R" \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"bet-$R\",\"playerId\":\"$PLAYER\",\"walletId\":\"$WALLET\",\"roundId\":\"r1\",\"gameId\":\"g1\",\"kind\":\"BET\",\"money\":{\"amount\":\"80.00\",\"currency\":\"BRL\"}}"
# provider-b, with its IAM key and its own token, refunds provider-a's BET in provider-a's name
aws_as provider-b sqs send-message --queue-url $QUEUE --message-group-id "$WALLET" --message-deduplication-id "spoof-$R" \
  --message-attributes "{\"accessToken\":{\"DataType\":\"String\",\"StringValue\":\"$PB\"}}" \
  --message-body "{\"messageId\":\"spoof-$R\",\"type\":\"WagerTransactionRequested\",\"occurredAt\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"data\":{\"providerId\":\"provider-a\",\"externalTransactionId\":\"spoof-$R\",\"idempotencyKey\":\"provider-a:spoof-$R\",\"playerId\":\"$PLAYER\",\"walletId\":\"$WALLET\",\"roundId\":\"r1\",\"gameId\":\"g1\",\"kind\":\"REFUND\",\"money\":{\"amount\":\"80.00\",\"currency\":\"BRL\"},\"referenceExternalTransactionId\":\"bet-$R\"}}"
aws_root sqs receive-message --queue-url http://ministack:4566/000000000000/wager-transactions-dlq.fifo \
  --message-attribute-names All --wait-time-seconds 5 --query 'Messages[].MessageAttributes.errorCode.StringValue'
curl -s localhost:8083/wallets/$WALLET -H "Authorization: Bearer $IT" | jq -c '{balance: .balance.amount, version}'
```

Esperado: `["PROVIDER_MISMATCH"]` na DLQ, e a carteira em `{"balance":"20.00","version":2}`. Antes da correção, o mesmo envio deixava a carteira em 100.00.

- [ ] **Passo 3: o fluxo legítimo.** Rodar o README §8.8 tal como ficou escrito, com o token. Esperado: o WIN com `"status":"PROCESSED"` e `"receivedVia":"SQS"`.

- [ ] **Passo 4: verificação final** (`superpowers:verification-before-completion`). Na mesma mensagem: a saída de `make check`, `make test-integration` e `make test-e2e`, os passos 1–3 desta tarefa e o `git status`.

- [ ] **Passo 5: proposta de commits** (Conventional Commits, sem trailer de coautoria), para o autor autorizar:
  1. `test(testkit): attach the provider token to sqs messages` (Tarefa 1).
  2. `feat(auth): verify tokens at an instant and report unavailable keys` (Tarefa 2).
  3. `feat(bootstrap): compose the auth module for the consumer role` (Tarefa 3).
  4. `feat(sqsconsumer): require the provider identity on every message` (Tarefa 4).
  5. `test: check the sqs token at send time and keep it out of the logs` (Tarefas 5 e 6).
  6. `docs: record the sqs provider identity (D-23)` (spec, plano, `decisions.md`, `messaging.md`, `transaction-lifecycle.md` e Tarefa 7).

# Identidade do provedor no SQS: design

**Data:** 01/10/2026 · **Caminho:** *architectural* ([`development-workflow.md`](../../development-workflow.md) §2): muda o contrato de entrada do SQS, a composição do Fx e a D-07 · **Status:** aprovada pelo autor em 01/10/2026; implementada pelo [plano](../plans/2026-10-01-sqs-provider-auth.md)

**Origem:** auditoria final contra o [`CHALLENGE.md`](../../../CHALLENGE.md), em 01/10/2026. No SQS, o `providerId` da mensagem não está ligado a nenhuma identidade autenticada. A lacuna estava registrada como limitação (`ARCHITECTURE.md` §16, item 2; D-07, parágrafo "SQS"), mas não fechada.
- **Reprodução:** num compose limpo, o `provider-b`, com a própria chave IAM, enviou um `REFUND` com `providerId: provider-a` contra a `BET` do A. O REFUND foi `PROCESSED` (`receivedVia: SQS`), e o saldo foi de 20.00 a 100.00. Não houve duplicidade nem saldo negativo, mas a operação mexeu numa transação de outro provedor.
- **O que o desafio pede (§2):** "A identidade autenticada deve determinar o `providerId` autorizado. Provedores acessam apenas suas próprias transações."
- **Risco:** o eliminatório E2 ("acesso não autorizado a operações ou transações") fica exposto pelo canal SQS.

**Implementa:**
- a D-23 (nova);
- as alterações da D-07 (parágrafo "SQS") e da D-12 (linha nova);
- [`messaging.md`](../../messaging.md) §2.1, §3.2, §4.1, §4.2, §4.4 e §8;
- [`transaction-lifecycle.md`](../../transaction-lifecycle.md) §3.2, §5.4, §6.2 e §8.

Esta spec registra só o **delta** em relação a `docs/`.

---

## 1. Objetivo e critério de pronto

**Objetivo:** no SQS, como no HTTP, o provedor é o da identidade autenticada pelo IdP.
- Uma mensagem só é processada se carregar um token do Keycloak válido **no instante do envio**, de um client com a role `provider`, cuja claim `provider_id` seja o `data.providerId`.
- Qualquer outra vai para a DLQ antes de qualquer leitura ou escrita de domínio.
- O **corpo** da mensagem continua o do desafio. O token vai num atributo da mensagem.

Fecha o E2 no SQS. O AUTH-09 passa a dizer: o acesso à fila é controlado pelo broker, e a identidade do provedor vem do IdP.

**Pronto quando** (evidência no chat, [`development-workflow.md`](../../development-workflow.md) §5):
1. `make check` verde.
2. `make test-integration` e `make test-e2e` verdes.
   - Os testes novos da §6 vistos falhando pelo motivo certo antes de passar.
   - Os testes escritos sobre comportamento existente com `// Sensitivity: …`.
3. O cenário da auditoria repetido com `docker compose up --build --wait`:
   - o `REFUND` do `provider-b` em nome do `provider-a` vai para a DLQ com `errorCode = PROVIDER_MISMATCH`;
   - saldo, versão e contagem de linhas ficam iguais;
   - o fluxo do README §8.8, agora com o token, continua `PROCESSED`.
4. Os documentos da §8 atualizados.

**Fora do escopo:**
- **HTTP com o IdP fora:** com uma chave desconhecida e o JWKS inacessível, o HTTP continua respondendo 401, como hoje. A classificação nova da decisão 6 só é usada pelo consumidor. Responder 503 no HTTP é um ajuste possível depois, que nenhum requisito pede.
- **`messageId` por provedor na inbox:** o desafio exige a unicidade de `(consumerName, messageId)`, então o `messageId` continua global.
  - Um provedor que reutilize o `messageId` de outro faz a mensagem do outro, se ela chegar depois, ir para a DLQ com `MESSAGE_HASH_MISMATCH`. Não há efeito financeiro.
  - Fica como limitação registrada. A mitigação é o produtor usar UUID.
- **Vínculo pelo `SenderId` do SQS e filas por provedor:** descartados (§9).
- **Gestão do token no produtor:** o produtor pede um token por `client_credentials` e o reutiliza enquanto for válido. O serviço não emite nem renova tokens.

---

## 2. Decisões

| # | Decisão | Motivo |
| --- | --- | --- |
| 1 | **Token no atributo de mensagem `accessToken`** (`DataType = String`). O valor é o access token JWT do `client_credentials`, sem o prefixo `Bearer`. O corpo não muda | O atributo é metadado de transporte, então fica fora do `message_hash` e do `payload_hash`: reenviar a mesma mensagem com um token novo continua sendo a mesma mensagem (duplicata na inbox ou replay). O envelope estrito (`DisallowUnknownFields`) e o contrato do desafio ficam intactos |
| 2 | **A mesma validação do HTTP** (D-07): RS256 pelo JWKS, `iss = OIDC_ISSUER`, `aud ∋ pda-api`, role `provider` válida só com a claim `provider_id`. O IdP, o realm e os clients são os mesmos, sem client novo | Um só modelo de identidade para os dois canais. Os provedores já têm o client |
| 3 | **A validade é avaliada no instante do envio**, o `SentTimestamp` gravado pelo broker (atributo de sistema, em ms), pedido no `ReceiveMessage`:<br>• aceito se `exp ≥ SentTimestamp − OIDC_CLOCK_SKEW` e, havendo `nbf`, se `nbf ≤ SentTimestamp + 5 min` (a tolerância fixa do go-oidc, a mesma do HTTP);<br>• sem `SentTimestamp` legível, vale o instante do recebimento (falha fechada) | O broker grava o `SentTimestamp`, então o produtor não o forja. Uma reentrega depois do `exp` do token (5 min no realm) continua válida, porque o token valia quando a mensagem entrou na fila; isso cobre retry, pausa por saúde e crash do consumidor. Comparar com o relógio do consumidor mandaria mensagens válidas para a DLQ depois de qualquer atraso |
| 4 | **Ordem no consumidor:**<br>1. autenticar o token e a role;<br>2. ler o envelope;<br>3. comparar o provedor;<br>4. chamar o caso de uso.<br>A comparação exige `data.providerId == token.provider_id` sempre que o campo vem no JSON, mesmo vazio. Só o campo ausente (ou `null`) segue para a validação sem estado do caso de uso, que o rejeita com `MISSING_FIELD`.<br>**Diferença consciente do HTTP:** lá o 400 da validação do corpo vem antes do 403. Aqui, uma mensagem que nomeia outro provedor é sempre `PROVIDER_MISMATCH`, mesmo que outro campo também seja inválido | A autenticação vem primeiro, como o 401 do HTTP. Comparar o provedor antes de validar o resto é mais restritivo e não expõe nada, porque o SQS não tem canal de resposta. Assim, nenhuma consulta de inbox, de idempotência ou de domínio acontece antes da autorização (AUTH-07), sem mudar o `app.ConsumeWager` |
| 5 | **Rejeições com os códigos do HTTP** ([`transaction-lifecycle.md`](../../transaction-lifecycle.md) §5.3), todos `CORRECTABLE`:<br>• `UNAUTHENTICATED`: token ausente, malformado, inválido ou expirado no envio;<br>• `FORBIDDEN`: sem a role `provider`, ou provedor sem `provider_id`;<br>• `PROVIDER_MISMATCH`.<br>A mensagem vai para a DLQ por envio explícito e é removida, sem inbox e sem escrita. Cada rejeição conta em `auth_failures_total{reason}` (os mesmos valores do HTTP) e em `sqs_dlq_sent_total{reason}`. No adaptador, as três são `apperrors.KindForbidden`, que o `decide` passa a mandar para a DLQ (hoje esse ramo cai no retry e não é alcançado) | São códigos estáveis que já existem, e a mensagem nunca vira efeito. Não há rótulo `channel` novo em `auth_failures_total`: o `sqs_dlq_sent_total` já separa o canal |
| 6 | **IdP indisponível é falha transitória.**<br>• O go-oidc v3.21.0 perde o tipo do erro da busca do JWKS: o `verify.go` o embrulha com `%v`.<br>• O `Verifier` passa a usar um `KeySet` próprio, que embrulha o `oidc.RemoteKeySet` e registra a falha de busca num marcador do contexto da chamada. Essa falha é o único erro do `RemoteKeySet` que embrulha uma causa (`jwks.go:178`, `fetching keys %w`).<br>• Com o marcador, `Authenticate` e `AuthenticateAt` devolvem `auth.ErrKeysUnavailable` em vez de `ErrUnauthenticated`.<br>• No consumidor, esse erro vira `KindTransient`: retry com backoff e sem DLQ | Sem isso, uma queda do Keycloak no momento em que o verificador precisa buscar chaves mandaria mensagens válidas para a DLQ como `UNAUTHENTICATED`. Isso acontece na primeira mensagem depois do start, porque o cache do go-oidc começa vazio, e numa rotação de chave. O §10 do desafio exige retry para falha transitória |
| 7 | **O `auth.Module` entra no grafo quando `HTTP_ENABLED` ou `CONSUMER_ENABLED`.** O `sqsconsumer` recebe o `*auth.Verifier` por uma porta própria (`Authenticator`). O fail fast do JWKS no start passa a valer também para uma instância só consumidora | O consumidor passa a depender do IdP como o HTTP. O [`structure.md`](../../structure.md) já permite que adaptadores importem `auth` |
| 8 | **O token nunca vai para o log nem para a cópia explícita da DLQ.** O envio explícito já copia só o corpo e os atributos próprios. A DLQ por redrive guarda a mensagem original, com o token | OBS-02. Um token que vaze da DLQ vale no máximo o tempo de vida do client (5 min) |
| 9 | **O `testkit` anexa o token por padrão:** o do provedor em `data.providerId` do corpo, ou o de `provider-a` se o corpo não tiver um. `SendOpts.Token` troca o token, e `SendOpts.NoToken` envia sem nenhum | Todo envio dos testes passa por `testkit.SendMessage`. Os testes que verificam o corpo, como o I04c, continuam afirmando os mesmos códigos |

---

## 3. Contrato da mensagem (delta em `messaging.md` §3.2)

Linha nova na tabela de atributos exigidos do produtor:

| Atributo SQS | Valor | Motivo |
| --- | --- | --- |
| Message attribute `accessToken` (obrigatório) | String: o access token JWT do client do provedor (`client_credentials` no realm `pda`), sem `Bearer` | A identidade do provedor (D-23). É validado como no HTTP, no instante do `SentTimestamp`, e fica fora dos hashes |

Exemplo de envio (README §8.8):

```sh
aws_as provider-a sqs send-message --queue-url $QUEUE \
  --message-group-id "$WALLET" --message-deduplication-id "msg-$R" \
  --message-attributes "{\"accessToken\":{\"DataType\":\"String\",\"StringValue\":\"$PROVIDER_TOKEN\"}}" \
  --message-body "…"
```

---

## 4. Componentes

### 4.1 `internal/auth`

```go
// ErrKeysUnavailable reports that the key set could not be fetched: the token
// was not judged, so the failure is transient (D-23).
var ErrKeysUnavailable = errors.New("auth: identity provider keys unavailable")

// AuthenticateAt verifies raw as Authenticate does, but evaluates exp and nbf
// at the instant at: the SentTimestamp of an SQS message (D-23).
func (v *Verifier) AuthenticateAt(ctx context.Context, raw string, at time.Time) (Principal, error)
```

- **`keyset.go` (novo):** `observedKeySet` implementa `oidc.KeySet` sobre o `*oidc.RemoteKeySet`. Quando a verificação falha com um erro que embrulha uma causa (a busca do JWKS), ele marca o registro posto no contexto pela chamada.
- **`Verifier`** tem dois `*oidc.IDTokenVerifier` sobre o mesmo `observedKeySet`:
  - o atual, com `Now = agora − OIDC_CLOCK_SKEW`, usado pelo `Authenticate`;
  - outro com `SkipExpiryCheck: true`, usado pelo `AuthenticateAt`, que confere `exp` (`IDToken.Expiry`) e `nbf` (claim lida do token) contra `at`.
- **A extração do `Principal`** (`azp`, `provider_id` e roles do `aud`) passa a ser uma função comum aos dois métodos.
- **Erros:** `ErrKeysUnavailable` quando o marcador foi registrado, senão `ErrUnauthenticated`. A causa fica na cadeia de erros, para os logs de debug.

### 4.2 `internal/adapters/sqsconsumer`

- **Porta nova:**
  ```go
  type Authenticator interface {
      AuthenticateAt(ctx context.Context, raw string, at time.Time) (auth.Principal, error)
  }
  ```
  O `NewConsumer` passa a recebê-la, e a interface `Metrics` ganha `AuthFailure(reason string)`, que o `*observability.Metrics` já implementa.
- **`ReceiveMessage`:** passa a pedir também o atributo de sistema `SentTimestamp` e o atributo de mensagem `accessToken`.
- **`authorize.go` (novo):**
  - `authenticate(ctx, a, msg, receivedAt) (auth.Principal, error)`: lê o `accessToken` e o `SentTimestamp` e exige a role `provider` (`auth.HasRole`);
  - `matchProvider(p, m) error`: compara o `data.providerId` com o provedor do token (`auth.ActsAs`);
  - erros: `UNAUTHENTICATED`, `FORBIDDEN` e `PROVIDER_MISMATCH` como `apperrors.KindForbidden` com o código; `auth.ErrKeysUnavailable` como `KindTransient`.
- **`handle`:** segue a ordem da decisão 4, dentro do prazo `SQS_PROCESSING_TIMEOUT` que já envolve o caso de uso. Cada rejeição de autorização é contada com o `reason` correspondente.
- **`decide`:** `KindForbidden` passa a gerar `actDLQ` com a categoria `CORRECTABLE`.

### 4.3 `internal/bootstrap`

`OptionsFor` inclui o `auth.Module` se `roles.HTTP || roles.Consumer`. O módulo `sqsconsumer` passa a injetar o `*auth.Verifier` como `Authenticator`.

### 4.4 `test/testkit`

`SendOpts` ganha `Token string` e `NoToken bool`, e o `SendMessage` anexa o `accessToken` conforme a decisão 9. O token vem do `testkit.Token` (`client_credentials` real), já usado pelos testes HTTP.

---

## 5. Fluxo (delta em `transaction-lifecycle.md` §6.2)

```
receber → autenticar o accessToken no SentTimestamp:
     ausente, inválido ou expirado no envio → DLQ (UNAUTHENTICATED)
     chaves do IdP indisponíveis            → transitória (backoff, sem DLQ)
     sem a role provider                    → DLQ (FORBIDDEN)
→ parsear o envelope (inválido → DLQ)
→ data.providerId presente e ≠ token.provider_id → DLQ (PROVIDER_MISMATCH)
→ calcular messageHash → inbox → validação sem estado → idempotência → uow (sem mudança)
```

| Resultado novo | Ação na mensagem |
| --- | --- |
| `UNAUTHENTICATED`, `FORBIDDEN`, `PROVIDER_MISMATCH` | `SendMessage` para a DLQ com `errorCode` e `errorCategory = CORRECTABLE`, seguido de `DeleteMessage`. Nada é gravado |
| Chaves do IdP indisponíveis | Transitória: `ChangeMessageVisibility` com backoff. O ping da pausa por saúde passa (o banco está no ar), então os pollers não pausam. Uma queda do IdP maior que o orçamento de tentativas (~18 min) leva a mensagem à DLQ pela redrive, e ela é reprocessada pelo reenvio do produtor |

---

## 6. Testes

| ID | Teste | Prova |
| --- | --- | --- |
| U33 | `TestAuthenticateAt` (`auth`, unitário; JWKS de teste com `httptest` e chave RSA gerada) | Um token já expirado agora, mas válido em `at`, é aceito. Um token expirado antes de `at − skew` é recusado com `ErrUnauthenticated`, assim como um `nbf` depois de `at + 5 min`. Assinatura forjada, `aud` errado e `iss` errado continuam recusados no método novo |
| U34 | `TestVerifierKeysUnavailable` (`auth`, unitário) | Token com `kid` fora do cache e JWKS respondendo 503: `ErrKeysUnavailable` em `Authenticate` e em `AuthenticateAt`. Com o JWKS de volta, o mesmo token é aceito. Token forjado com o JWKS no ar: `ErrUnauthenticated`. *Sensibilidade:* sem o `observedKeySet`, o primeiro caso vira `ErrUnauthenticated` |
| U35 | `TestAuthorizeMessage` (`sqsconsumer`, unitário, com um `Authenticator` de teste) | Tabela: sem atributo, token recusado, chaves indisponíveis, sem role, `provider_id ≠ data.providerId`, `data.providerId` vazio (`PROVIDER_MISMATCH`), `data.providerId` de outro provedor com outro campo inválido (`PROVIDER_MISMATCH`), `data.providerId` ausente e caso válido. Afirma a ação (DLQ com o código, retry ou processamento) e que o processador só é chamado nos dois últimos casos. Também afirma que o `SentTimestamp` da mensagem é o instante passado ao `AuthenticateAt` e que, sem ele, vale o `receivedAt` |
| — | `TestDecide` (`sqsconsumer`, existente, sem ID no `test-plan`) | Linha nova: `KindForbidden` gera DLQ com a categoria `CORRECTABLE` |
| A05 | `TestSQSProviderIdentity` (`test/integration`, PostgreSQL, Keycloak e MiniStack reais; sem paralelismo, por causa do `SnapshotCounts`) | O cenário da auditoria: o `provider-b`, com o próprio token, envia um `REFUND` com `providerId: provider-a` contra a `BET` de A, e a mensagem vai para a DLQ com `PROVIDER_MISMATCH`. Também: sem token, `UNAUTHENTICATED`; token do `wallet-service`, `FORBIDDEN`; token forjado, `UNAUTHENTICATED`. Em todos os casos, `SnapshotCounts` não muda e a carteira fica intacta. A fila é a isolada do pacote; a permissão IAM de envio continua provada pelo I04f, e o cenário com a chave IAM do `provider-b` é o item 3 do critério de pronto, no compose. *Sensibilidade:* sem a comparação do provedor, o `REFUND` é `PROCESSED` (o achado da auditoria) |
| A06 | `TestTokenCheckedAtSendTime` (`sqsconsumer`, integração; o consumidor só é ligado depois do envio) | Uma mensagem enviada com o token do `provider-short-lived` (5 s de vida), com o consumidor ligado só depois do `exp` + tolerância, é `PROCESSED`. Um token já expirado no envio vai para a DLQ com `UNAUTHENTICATED`. *Sensibilidade:* comparando com o relógio do consumidor, a primeira mensagem vai para a DLQ |
| I14 | `TestLogsHaveIdsWithoutSecrets` (existente) | Ganha um token marcado no atributo do SQS. Nenhuma linha de log o contém |
| I07c / I27 | `TestFxFailFast`, `TestFxRoles` (existentes) | Só com `CONSUMER_ENABLED`, o grafo tem o `auth`, e o start falha com o JWKS inacessível |

**Regressão:** todos os testes que enviam ao SQS (I04a–f, `TestSQSEndToEnd`, C01b, C05a/b, C07a/b, C08a, C10a/b, R01, R03) continuam verdes com o token anexado pelo `testkit`. Os testes de integração do `sqsconsumer` passam a montar o `Consumer` com o `Verifier` real e tokens reais.

---

## 7. Requisitos no encerramento ([`delivery-requirements.md`](../../delivery-requirements.md))

- **AUTH-09:** reescrito: o broker controla o acesso à fila, e o IdP dá a identidade do provedor na mensagem. Evidência: A05, A06 e I04f.
- **AUTH-04 e AUTH-05:** ganham "inclusive pelo SQS", com o A05.
- **AUTH-07, TST-A02 e TST-A03:** o A05 prova que um acesso negado pelo SQS não tem efeito.
- **Tabela §0:** a linha E2 cita o A05.
- **OBS-02:** I14 estendido.
- **FX-01 e FX-02:** `auth` no papel de consumidor (I07c, I27).

---

## 8. Ajustes em `docs/`

**Aplicados junto com esta spec** (decisão e contrato, [`development-workflow.md`](../../development-workflow.md) §3.1):

| Documento | Ajuste |
| --- | --- |
| [`decisions.md`](../../decisions.md) | D-23 nova e a linha no resumo. D-07: o parágrafo "SQS" passa a apontar para a D-23. D-12: linha "Identidade do provedor (D-23)" |
| [`messaging.md`](../../messaging.md) | §2.1: a limitação do provedor sai, e a identidade vem do IdP. §3.2: o atributo `accessToken`. §4.1: o `ReceiveMessage` pede o `SentTimestamp`. §4.2: as ações novas. §4.4: as origens novas da DLQ. §8: o `auth_failures_total` também conta o SQS |
| [`transaction-lifecycle.md`](../../transaction-lifecycle.md) | §3.2: a autorização no SQS. §5.4: os códigos de autorização na DLQ. §6.2: o fluxo da §5. §8: o IdP indisponível no SQS é transitório |

**No encerramento** (descrevem o sistema entregue, então só mudam com o código):
- `ARCHITECTURE.md`:
  - §9.1 e §10.3 (mensageria);
  - §15, item 16, que se inverte;
  - §16: o item 2 sai; os itens novos são o token na DLQ por redrive, a queda do IdP além do orçamento de tentativas e o `messageId` global entre provedores;
  - §17.
- `README.md`: §5 (o reprocessamento exige o reenvio pelo produtor, com um token novo) e §8.8 (o envio com o token).
- `docs/getting-started.md`, passo 19.
- `test-plan.md`: U33–U35, A05, A06, I14, I07c e I27.
- `delivery-requirements.md` (§7 desta spec).
- `structure.md`: `auth/keyset.go` e `sqsconsumer/authorize.go`.
- A descrição do painel de `auth_failures_total` em `deploy/grafana/dashboards/pda.json`.
- `implementation-plan.md` e `dev/diary.md`.

---

## 9. Alternativas descartadas

| Alternativa | Por que não |
| --- | --- |
| **Vincular pelo `SenderId` do SQS** (o principal IAM do remetente) | Na AWS, funcionaria com um mapa do usuário IAM para o provedor. **O MiniStack devolve o id da conta** (`000000000000`), não o do usuário (`AIDA…`): sondado em 01/10/2026 com uma mensagem enviada como `provider-a`. Não seria demonstrável nem testável localmente |
| **Uma fila por provedor** | O desafio nomeia `wager-transactions.fifo`. Mudaria o provisionamento, o consumidor, as políticas e a DLQ |
| **Assinatura HMAC por provedor** | Exigiria um segredo compartilhado novo e uma gestão de chaves própria, quando o IdP já resolve a identidade |
| **Token no corpo** | Muda o contrato do desafio e o envelope estrito. Ou entraria no hash, o que torna um reenvio com token novo um `MESSAGE_HASH_MISMATCH`, ou exigiria uma exceção na regra do hash |

---

## 10. Riscos

| Risco | Mitigação |
| --- | --- |
| A detecção da decisão 6 depende de um detalhe do go-oidc (só a busca do JWKS embrulha uma causa) | O U34 roda contra a biblioteca real: uma atualização que mude o detalhe quebra o teste, não a produção. A versão está fixada no `go.mod` |
| Ligar a exigência quebra de uma vez todos os testes que enviam ao SQS | O plano muda o `testkit` (decisão 9) **antes** de o consumidor exigir o token. Anexar o atributo é inofensivo enquanto ninguém o lê |
| Prazo: hoje é o dia da entrega | Tarefas pequenas, e cada checkpoint deixa o sistema entregável: testkit → `auth` → consumidor + A05 → classificação transitória → documentos. Se o tempo acabar depois do A05 verde, a lacuna eliminatória já está fechada e o resto vira trabalho registrado |
| Mensagens sem token em voo no momento do deploy vão para a DLQ com `UNAUTHENTICATED` | Em produção, os produtores passam a anexar o token antes de o consumidor exigi-lo. Localmente, as filas começam vazias. Registrado no README §5 |

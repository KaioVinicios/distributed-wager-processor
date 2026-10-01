# Guia rápido: entender, rodar e validar

Para quem está chegando agora e quer, em uns 15 minutos, entender o que o projeto faz, subir tudo na própria máquina e **ver com os próprios olhos** que ele cumpre o que promete. A versão completa e técnica está no [`README.md`](../README.md) e no [`ARCHITECTURE.md`](../ARCHITECTURE.md).

---

## 1. O que é, em 1 minuto

Pense num cassino online. O **jogador** tem uma **carteira** com saldo. Os jogos são de outras empresas, os **provedores**, que avisam o cassino a cada jogada: "o jogador apostou R$ 30", "ganhou R$ 50", "cancela aquela aposta". O **pda** recebe esses avisos e atualiza o saldo, sem nunca perder, duplicar ou inventar dinheiro.

Os avisos (as **operações**) são de 5 tipos:

| Tipo | O que significa | Efeito no saldo |
| --- | --- | --- |
| `BET` | O jogador apostou | Tira dinheiro (se houver saldo) |
| `WIN` | O jogador ganhou | Põe dinheiro |
| `LOSS` | O jogador perdeu a rodada | Nenhum (a aposta já foi descontada no `BET`), valor sempre `0.00` |
| `REFUND` | Devolve uma aposta (ex.: jogo cancelado) | Põe de volta o valor do `BET` |
| `ROLLBACK` | Desfaz uma operação anterior (`BET`, `WIN` ou `REFUND`) | O contrário da operação desfeita |

### O que o projeto promete

| Promessa | Em palavras simples | Onde você confere (§4) |
| --- | --- | --- |
| Saldo correto | Dinheiro é texto com 2 casas (`"30.00"`), nunca número quebrado | Passos 2 a 12 e 15 |
| Sem duplicar | Mandar a mesma operação duas vezes não desconta duas vezes | Passo 5 |
| Não aceitar trapaça | Mesma chave com outro conteúdo é recusada | Passo 6 |
| Não deixar negativo | Aposta maior que o saldo é rejeitada | Passo 7 |
| Desfazer só uma vez | Um segundo `ROLLBACK` da mesma operação é recusado | Passo 9 |
| Aguentar fora de ordem | Um `REFUND` que chega antes da aposta espera por ela | Passos 10 a 12 |
| Histórico que não se apaga | Todo movimento vira uma linha no extrato (*ledger*), que só cresce | Passo 14 |
| Saldo auditável | Dá para recalcular o saldo a partir do extrato e comparar | Passo 15 |
| Segurança | Sem token, ou com o token errado, não entra | Passos 16 a 18 |
| Dois canais, mesmo resultado | A operação pode chegar por HTTP ou por fila (SQS) | Passo 19 |
| Várias instâncias | 3 cópias do serviço rodam juntas e dão o mesmo resultado | Os passos alternam as portas 8081, 8082 e 8083 |
| Avisar o resto do sistema | Cada mudança gera um evento publicado para outros sistemas | §5 |

---

## 2. As peças

```
                   pede o token (o "crachá")
  Provedor ─────────────────────────────▶ Keycloak :8080
  (provider-a)
      │ HTTP  POST /wagering/transactions        ┌──────────────────────┐
      ├─────────────────────────────────────────▶│  pda (3 réplicas)    │──▶ PostgreSQL :5432
      │ SQS   fila wager-transactions.fifo       │  :8081 :8082 :8083   │    saldo, operações,
      └─────────────────────────────────────────▶│                      │    extrato (ledger)
                                                 └──────────┬───────────┘
  Serviço interno (wallet-service) ──HTTP──▶ abre carteira, │
  consulta saldo e extrato, reconcilia                      ▼
                                          SNS wallet-events.fifo ──▶ fila wallet-events-audit.fifo
                                          (eventos para outros sistemas)
```

| Peça | Papel |
| --- | --- |
| **pda** (`app-1`, `app-2`, `app-3`) | O serviço em Go. As 3 réplicas são iguais e podem receber qualquer operação |
| **PostgreSQL** | O banco. Guarda carteiras, operações e o extrato. É a fonte da verdade |
| **Keycloak** | Emite os tokens. Quem não tem token válido não usa a API |
| **MiniStack** | Simula a AWS na sua máquina: a fila de entrada (SQS) e o tópico de eventos (SNS) |

**Onde fica o código:**

| Pasta | O que tem |
| --- | --- |
| `cmd/pda/` | O ponto de entrada do programa |
| `internal/domain/` | As regras de negócio puras (dinheiro, carteira, operações), sem banco nem rede |
| `internal/app/` | Os casos de uso: "processar uma operação", "abrir carteira", "reconciliar" |
| `internal/adapters/` | A ligação com o mundo: HTTP, PostgreSQL, SQS, SNS |
| `migrations/` | O SQL que cria as tabelas |
| `api/` | O contrato da API (`openapi.yaml`) e dos eventos (`events.yaml`) |
| `test/e2e/` | Testes com as 3 réplicas rodando de verdade, incluindo quedas |

---

## 3. Subir tudo

Você precisa de **Docker** (com Compose 2.24 ou mais novo), `curl`, `jq` e `uuidgen`. Go só é necessário para rodar os testes (§6).

```sh
git clone https://github.com/KaioVinicios/pda.git
cd pda
docker compose up --build --wait
```

A primeira vez leva alguns minutos (baixa imagens e compila). Não há configuração manual: o banco, as filas e os usuários são criados sozinhos. Para conferir:

```sh
curl -s localhost:8081/health/ready
```

**Esperado:** `{"status":"UP","checks":{"postgres":"UP","sqs":"UP"}}`

> **Prefere clicar a digitar?** Abra o Swagger UI em <http://localhost:8081/docs>: ele pede o token e envia as requisições pelo navegador. Outra opção é a coleção [`api/requests.http`](../api/requests.http) (VS Code com REST Client, ou JetBrains).

**Para desligar:** `docker compose down -v` (apaga os dados também).

---

## 4. Testar na mão: uma rodada completa

Rode os blocos **no mesmo terminal**, em ordem, a partir da pasta do projeto. Os valores de `transactionId`, `id` e datas mudam a cada execução; o resto deve bater.

### 4.1 Preparar

```sh
PROVIDER_TOKEN=$(scripts/get-token.sh provider-a)       # token do provedor
INTERNAL_TOKEN=$(scripts/get-token.sh wallet-service)   # token do serviço interno
PLAYER=$(uuidgen | tr 'A-Z' 'a-z')                      # um jogador novo
R=$(date +%s)                                           # sufixo único, para poder repetir o roteiro
```

Os tokens valem **5 minutos**. Se algo começar a responder 401, rode essas duas primeiras linhas de novo.

### 4.2 Passo 1: abrir a carteira com R$ 100,00

```sh
WALLET=$(curl -s -X POST localhost:8081/wallets \
  -H "Authorization: Bearer $INTERNAL_TOKEN" \
  -H "Content-Type: application/json" \
  -d @- <<EOF | tee /dev/stderr | jq -r .id
{
  "playerId": "$PLAYER",
  "initialBalance": { "amount": "100.00", "currency": "BRL" }
}
EOF
)
```

**Esperado** (HTTP 201):

```json
{"id":"01a0f460-…","playerId":"dca35d4a-…","balance":{"amount":"100.00","currency":"BRL"},"version":1,"createdAt":"…","updatedAt":"…"}
```

### 4.3 Passo 2: a primeira aposta, com o payload completo

```sh
curl -s -w '\nHTTP %{http_code}\n' -X POST localhost:8081/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: provider-a:bet-1-$R" \
  -d @- <<EOF
{
  "providerId": "provider-a",
  "externalTransactionId": "bet-1-$R",
  "playerId": "$PLAYER",
  "walletId": "$WALLET",
  "roundId": "round-1",
  "gameId": "fortune-chimp",
  "kind": "BET",
  "money": { "amount": "30.00", "currency": "BRL" }
}
EOF
```

**Esperado** (HTTP 200): o saldo caiu de 100,00 para 70,00.

```json
{"transactionId":"01a0f460-12b8-…","status":"PROCESSED","balance":{"amount":"70.00","currency":"BRL"},"idempotentReplay":false}
```

O que cada campo do payload quer dizer:

| Campo | Significado |
| --- | --- |
| `providerId` | Quem está enviando. Precisa ser o mesmo do token |
| `externalTransactionId` | O id da operação **no sistema do provedor** |
| `Idempotency-Key` (cabeçalho) | A "chave anti-duplicação". Mesma chave = mesma operação |
| `playerId`, `walletId` | De quem é o dinheiro |
| `roundId`, `gameId` | Qual rodada e qual jogo |
| `kind` | O tipo da operação (§1) |
| `money` | Valor e moeda. O valor é **texto com exatamente 2 casas**: `"30.00"`, nunca `30` ou `30.5` |
| `referenceExternalTransactionId` | (só em `WIN`, `REFUND` e `ROLLBACK`) qual operação anterior está sendo ganha, devolvida ou desfeita |

### 4.4 Passos 3 a 12: o resto da rodada

Para não repetir o payload inteiro, esta função monta o mesmo corpo do passo 2 trocando só o que muda:

```sh
# jogar <porta> <tipo> <id da operação> <valor> [id da operação referenciada]
jogar() {
  local ref=""
  [ -n "${5:-}" ] && ref=",\"referenceExternalTransactionId\":\"$5-$R\""
  curl -s -w '  HTTP %{http_code}\n' -X POST "localhost:$1/wagering/transactions" \
    -H "Authorization: Bearer $PROVIDER_TOKEN" -H "Content-Type: application/json" \
    -H "Idempotency-Key: provider-a:$3-$R" \
    -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"$3-$R\",\"playerId\":\"$PLAYER\",\"walletId\":\"$WALLET\",\"roundId\":\"round-1\",\"gameId\":\"fortune-chimp\",\"kind\":\"$2\",\"money\":{\"amount\":\"$4\",\"currency\":\"BRL\"}$ref}"
}
```

Rode um comando por vez e compare com a coluna "Esperado". A porta muda de propósito: cada uma é uma réplica diferente.

| # | Comando | O que está sendo testado | Esperado |
| --- | --- | --- | --- |
| 3 | `jogar 8082 WIN win-1 50.00 bet-1` | Ganho da aposta do passo 2 | 200, `PROCESSED`, saldo **120.00** |
| 4 | `jogar 8083 LOSS loss-1 0.00` | Perda não mexe no saldo | 200, `PROCESSED`, saldo **120.00** |
| 5 | `jogar 8083 BET bet-1 30.00` | **Repetir** o passo 2, em outra réplica | 200, `"idempotentReplay":true`, saldo **70.00** (o resultado original, sem descontar de novo) |
| 6 | `jogar 8081 BET bet-1 1.00` | Mesma chave, conteúdo diferente | 409, `IDEMPOTENCY_KEY_REUSED` |
| 7 | `jogar 8081 BET bet-2 500.00` | Aposta maior que o saldo | 422, `REJECTED`, `INSUFFICIENT_FUNDS`, saldo **120.00** |
| 8 | `jogar 8082 ROLLBACK rb-1 50.00 win-1` | Desfazer o ganho do passo 3 | 200, `PROCESSED`, saldo **70.00** |
| 9 | `jogar 8083 ROLLBACK rb-2 50.00 win-1` | Desfazer o mesmo ganho de novo | 422, `REJECTED`, `ALREADY_REVERSED`, saldo **70.00** |
| 10 | `jogar 8081 REFUND refund-3 10.00 bet-3` | Devolução de uma aposta que **ainda não chegou** | 202, `PENDING_REFERENCE` (fica esperando) |
| 11 | `jogar 8082 BET bet-3 10.00` | A aposta chega depois | 200, `PROCESSED`, saldo **60.00** |

**Passo 12:** um segundo depois, o `REFUND` que estava esperando é concluído sozinho, em segundo plano:

```sh
sleep 1
curl -s localhost:8083/providers/provider-a/wagering/transactions/refund-3-$R \
  -H "Authorization: Bearer $PROVIDER_TOKEN" | jq -c '{kind, status, balance}'
```

**Esperado:** `{"kind":"REFUND","status":"PROCESSED","balance":{"amount":"70.00","currency":"BRL"}}`

Exemplos de resposta, para comparar o formato:

```text
# passo 5 (replay): mesmo transactionId do passo 2
{"transactionId":"01a0f460-12b8-…","status":"PROCESSED","balance":{"amount":"70.00","currency":"BRL"},"idempotentReplay":true}  HTTP 200

# passo 7 (rejeição de negócio): fica registrada e pode ser consultada depois
{"transactionId":"01a0f460-132c-…","status":"REJECTED","balance":{"amount":"120.00","currency":"BRL"},"failureCode":"INSUFFICIENT_FUNDS","failureCategory":"DEFINITIVE","idempotentReplay":false}  HTTP 422

# passo 6 (erro do cliente): formato padrão de erro, o problem+json
{"type":"about:blank","title":"Conflict","status":409,"code":"IDEMPOTENCY_KEY_REUSED","category":"CORRECTABLE","detail":"The idempotency key was already used with a different payload.","correlationId":"…"}  HTTP 409
```

### 4.5 Passos 13 a 15: conferir saldo, extrato e reconciliação

```sh
# 13. saldo atual
curl -s localhost:8082/wallets/$WALLET -H "Authorization: Bearer $INTERNAL_TOKEN" | jq -c '{balance, version}'
```

**Esperado:** `{"balance":{"amount":"70.00","currency":"BRL"},"version":6}`. A versão sobe 1 a cada movimento de dinheiro: abertura, `BET`, `WIN`, `ROLLBACK`, `BET` e `REFUND`. O `LOSS` e as rejeições não contam.

```sh
# 14. extrato (ledger)
curl -s localhost:8083/wallets/$WALLET/ledger -H "Authorization: Bearer $INTERNAL_TOKEN" \
  | jq -c '.items[] | {direction, amount: .amount.amount, balanceAfter: .balanceAfter.amount, walletVersion}'
```

**Esperado:** 6 linhas, que contam a história inteira:

```text
{"direction":"CREDIT","amount":"100.00","balanceAfter":"100.00","walletVersion":1}   abertura
{"direction":"DEBIT","amount":"30.00","balanceAfter":"70.00","walletVersion":2}      BET
{"direction":"CREDIT","amount":"50.00","balanceAfter":"120.00","walletVersion":3}    WIN
{"direction":"DEBIT","amount":"50.00","balanceAfter":"70.00","walletVersion":4}      ROLLBACK do WIN
{"direction":"DEBIT","amount":"10.00","balanceAfter":"60.00","walletVersion":5}      BET que chegou atrasada
{"direction":"CREDIT","amount":"10.00","balanceAfter":"70.00","walletVersion":6}     REFUND que esperou
```

```sh
# 15. reconciliação: recalcula o saldo somando o extrato e compara com o saldo guardado
curl -s -X POST localhost:8081/wallets/$WALLET/reconciliation -H "Authorization: Bearer $INTERNAL_TOKEN" | jq -c
```

**Esperado:** `"consistent":true` e diferença zero.

```json
{"walletId":"…","storedBalance":{"amount":"70.00","currency":"BRL"},"calculatedBalance":{"amount":"70.00","currency":"BRL"},"difference":{"amount":"0.00","currency":"BRL"},"consistent":true,"checkedEntries":6}
```

### 4.6 Passos 16 a 18: segurança e validação

| # | Comando | Esperado |
| --- | --- | --- |
| 16 | `curl -s localhost:8081/wallets/$WALLET` (sem token) | 401, `UNAUTHENTICATED` |
| 17 | `curl -s -o /dev/null -w '%{http_code}\n' localhost:8081/wallets/$WALLET -H "Authorization: Bearer $PROVIDER_TOKEN"` (provedor numa rota interna) | `403` |
| 18 | `jogar 8081 BET bad-1 10.5` (valor com 1 casa) | 400, `INVALID_AMOUNT`, `"field":"money.amount"` |

Um erro 400 não é gravado: nada mudou no saldo nem no extrato.

### 4.7 Passo 19: a mesma operação pela fila (SQS)

O provedor coloca a mensagem na fila com as próprias credenciais da AWS simulada e anexa o próprio token do Keycloak, como no HTTP. Alguma das 3 réplicas a processa:

```sh
aws_as() {   # AWS CLI via Docker, como um usuário da AWS simulada
  local profile=$1; shift
  docker run --rm --network pda_default -v "$PWD/.local/aws:/aws:ro" \
    -e AWS_SHARED_CREDENTIALS_FILE=/aws/credentials -e AWS_PROFILE="$profile" -e AWS_REGION=us-east-1 \
    amazon/aws-cli:2.36.31 --endpoint-url http://ministack:4566 "$@"
}

aws_as provider-a sqs send-message \
  --queue-url http://ministack:4566/000000000000/wager-transactions.fifo \
  --message-group-id "$WALLET" --message-deduplication-id "msg-$R" \
  --message-attributes "{\"accessToken\":{\"DataType\":\"String\",\"StringValue\":\"$(scripts/get-token.sh provider-a)\"}}" \
  --message-body "$(cat <<EOF
{
  "messageId": "msg-$R",
  "type": "WagerTransactionRequested",
  "occurredAt": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "sqs-1-$R",
    "idempotencyKey": "provider-a:sqs-1-$R",
    "playerId": "$PLAYER",
    "walletId": "$WALLET",
    "roundId": "round-1",
    "gameId": "fortune-chimp",
    "kind": "WIN",
    "money": { "amount": "5.00", "currency": "BRL" }
  }
}
EOF
)"

sleep 2
curl -s localhost:8082/providers/provider-a/wagering/transactions/sqs-1-$R \
  -H "Authorization: Bearer $PROVIDER_TOKEN" | jq -c '{kind, status, receivedVia, balance}'
```

**Esperado:** `{"kind":"WIN","status":"PROCESSED","receivedVia":"SQS","balance":{"amount":"75.00","currency":"BRL"}}`

O `data` da mensagem é o mesmo payload do HTTP, com a chave anti-duplicação dentro dele (`idempotencyKey`) em vez de no cabeçalho. O token vai num atributo da mensagem (`accessToken`), fora do corpo. As credenciais da AWS só dizem que o provedor pode enviar; é o token que diz **qual** provedor ele é. Sem token, ou com o token de outro provedor, a mensagem vai para a fila de erros (DLQ) sem mexer em nenhum saldo.

---

## 5. O que é entregue do outro lado: os eventos

Cada mudança gera eventos, gravados junto com a operação e depois publicados no tópico `wallet-events.fifo`. Outros sistemas (antifraude, relatórios, notificações) assinam esse tópico. No ambiente local, a fila `wallet-events-audit.fifo` faz esse papel. Para ler os eventos da sua carteira:

```sh
docker run --rm --network pda_default -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test -e AWS_REGION=us-east-1 \
  amazon/aws-cli:2.36.31 --endpoint-url http://ministack:4566 sqs receive-message \
  --queue-url http://ministack:4566/000000000000/wallet-events-audit.fifo \
  --max-number-of-messages 10 --wait-time-seconds 10 --query 'Messages[].Body' --output json \
  | jq -r --arg w "$WALLET" '.[]? | fromjson | select(.data.walletId == $w)
      | [.eventType, (.data.transactionKind // .data.kind), (.data.balanceAfter.amount // "-")] | @tsv'
```

**Esperado** (trecho; a ordem entre eventos pode variar um pouco, porque 3 réplicas publicam ao mesmo tempo):

```text
WagerTransactionProcessed   OPENING    100.00
WalletBalanceChanged        OPENING    100.00
WagerTransactionProcessed   BET        70.00
WalletBalanceChanged        BET        70.00
WagerTransactionProcessed   WIN        120.00
WalletBalanceChanged        WIN        120.00
WagerTransactionProcessed   LOSS       120.00
WagerTransactionRejected    BET        -
WagerTransactionProcessed   ROLLBACK   70.00
WalletBalanceChanged        ROLLBACK   70.00
```

- **Se não aparecer nada, rode de novo** (num ambiente já usado, pode levar 2 ou 3 tentativas). A fila entrega primeiro os eventos mais antigos do ambiente, de outras execuções, e o comando só mostra os da sua carteira. Os eventos lidos não são apagados: voltam para a fila depois de 30 s.
- **Os 4 tipos de evento:** `WagerTransactionProcessed` (operação aceita), `WalletBalanceChanged` (o saldo mudou, com o antes e o depois), `WagerTransactionRejected` (operação recusada) e `WagerTransactionPendingReference` (operação esperando a referência).
- Um evento completo tem esta cara:

```json
{
  "eventId": "01a0f460-12e1-…",
  "eventType": "WalletBalanceChanged",
  "version": 1,
  "occurredAt": "2026-09-30T22:12:12.126Z",
  "aggregateType": "Wallet",
  "aggregateId": "01a0f460-1283-…",
  "correlationId": "01a0f460-12c9-…",
  "data": {
    "walletId": "01a0f460-1283-…",
    "transactionId": "01a0f460-12df-…",
    "transactionKind": "WIN",
    "direction": "CREDIT",
    "money": { "amount": "50.00", "currency": "BRL" },
    "balanceBefore": { "amount": "70.00", "currency": "BRL" },
    "balanceAfter": { "amount": "120.00", "currency": "BRL" },
    "walletVersion": 3
  }
}
```

O contrato formal dos eventos está em [`api/events.yaml`](../api/events.yaml).

**Métricas:** cada réplica conta o que processou, em `localhost:9091/metrics`, `9092` e `9093`:

```sh
for p in 9091 9092 9093; do curl -s localhost:$p/metrics | grep '^wager_transactions_total'; done
```

**Dashboard:** abra <http://localhost:3000>. O Grafana mostra as métricas das 3 réplicas somadas em gráficos (operações por status, outbox, fila, reconciliação), sem login. Depois de cada passo deste roteiro, os contadores sobem em até 5 s.

---

## 6. Testes automáticos

| Comando | O que testa | Precisa de | Tempo |
| --- | --- | --- | --- |
| `go test ./...` | As regras de negócio isoladas (unitários) | Só Go 1.27.1 | ~15 s |
| `make check` | Tudo acima com `-race`, mais formatação, lint e `go vet` | Go e Docker | ~15 s com cache |
| `make test-integration` | Os fluxos com PostgreSQL, Keycloak e AWS simulada de verdade | Docker (sobe a infraestrutura sozinho) | ~1 min |
| `make test-e2e` | 3 réplicas do binário ao mesmo tempo, com quedas, banco pausado e desligamento | Docker | ~2,5 min |

**Deu certo quando:** cada pacote termina com `ok` (ou `?` quando não tem teste) e nenhuma linha diz `FAIL`:

```text
ok  	github.com/KaioVinicios/pda/internal/domain/wagering	0.412s
ok  	github.com/KaioVinicios/pda/internal/domain/wallet	0.221s
?   	github.com/KaioVinicios/pda/migrations	[no test files]
```

- **Não rode `make test-integration` e `make test-e2e` ao mesmo tempo:** os dois usam o mesmo banco e a mesma fila, e alguns testes pausam esses serviços de propósito.
- Qual teste comprova cada requisito do desafio: [`delivery-requirements.md`](delivery-requirements.md).

---

## 7. Deu problema?

| Sintoma | O que fazer |
| --- | --- |
| `address already in use` ao subir | Alguma porta está ocupada (`5432`, `8080`, `4566`, `8081`–`8083`, `9091`–`9093`, `9090`, `3000`). Pare o outro programa |
| 401 com um token que funcionava | O token expirou (5 min). Rode `PROVIDER_TOKEN=$(scripts/get-token.sh provider-a)` de novo |
| 409 `WALLET_ALREADY_EXISTS` no passo 1 | O jogador já tem carteira. Gere outro: `PLAYER=$(uuidgen \| tr 'A-Z' 'a-z')` |
| As réplicas reiniciam sem parar depois de recriar o MiniStack | `docker compose up aws-init && docker compose restart app-1 app-2 app-3` |
| `/docs` abre em branco | O Swagger UI vem da internet. Sem conexão, use [`api/requests.http`](../api/requests.http) |
| Ver o que o serviço está fazendo | `docker compose logs -f app-1` |

Mais casos no [`README.md`](../README.md) §10.

---

## 8. Para ir além

| Quero entender… | Leia |
| --- | --- |
| Todas as variáveis, filas e identidades de teste | [`README.md`](../README.md) |
| Por que cada decisão técnica foi tomada | [`ARCHITECTURE.md`](../ARCHITECTURE.md) |
| Todos os estados de uma operação e todos os códigos de erro | [`transaction-lifecycle.md`](transaction-lifecycle.md) |
| Como funcionam as filas, a DLQ e os eventos | [`messaging.md`](messaging.md) |
| As tabelas do banco | [`data-model.md`](data-model.md) |
| Como os testes estão organizados | [`testing.md`](testing.md) |

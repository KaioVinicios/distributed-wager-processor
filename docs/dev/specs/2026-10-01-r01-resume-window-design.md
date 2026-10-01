# R01 intermitente: a janela da queda medida depois do `unpause`: design

**Data:** 01/10/2026 · **Caminho:** correção pontual (*bounded*, [`development-workflow.md`](../../development-workflow.md) §2), spec e plano curtos · **Status:** aprovada pelo autor em 01/10/2026, junto com o plano ("corrija, teste novamente")

**Origem:** na terceira de três rodadas completas da suíte, pedidas antes dos commits da D-23, o `make test-e2e` falhou em `TestPostgresOutage` (R01): `resilience_test.go:212: answer during the outage = 200 "" Retry-After "" (transport error <nil>), want 503 TEMPORARILY_UNAVAILABLE with Retry-After 1`, quatro vezes. As outras 5 execuções do R01 no mesmo dia passaram.

---

## 1. Investigação (causa raiz)

**Evidências:**

1. **O produto se comportou como esperado.** No log da instância 0:
   - o PostgreSQL congelou por volta de 13:30:32;
   - o `/health/ready` foi a 503 às 13:30:34;
   - o consumidor pausou às 13:30:36;
   - as requisições responderam 503 depois do prazo de 5 s (`HTTP_REQUEST_TIMEOUT` do e2e).

   Os erros de SQS e SNS no começo do mesmo log são do teste anterior (R02, que congela o MiniStack), porque o log de uma instância cobre toda a sua execução.
2. **As 4 respostas 200 eram requisições presas no banco.** Entre 13:30:47.73 e 13:30:47.79, oito requisições da instância 0 terminaram com 200 e durações de 0,5 s a 4,7 s. Todas foram liberadas no mesmo instante: o do `unpause`.
3. **A janela é medida tarde.** O R01 faz `resume()` e só depois `resumedAt := time.Now()`. O `docker compose unpause` libera o container antes de o comando retornar, então uma requisição que estava esperando o banco pode concluir com 200 entre o `unpause` ter efeito e o `resumedAt`. Ela entra na conta "durante a queda", embora tenha terminado depois dela.
4. **Experimento:** com um atraso artificial de 1,5 s entre o `resume()` e o `resumedAt`, o R01 falhou na primeira execução, com o mesmo sintoma, 32 vezes. O atraso foi revertido em seguida.

**Causa raiz:** o fim da janela da queda é marcado depois do `resume()`, e não antes. A falha depende de quanto o `docker compose unpause` demora para retornar, por isso é rara. O início da janela não tem o problema: o teste já ignora o primeiro segundo depois do `pause`.

**Relação com a D-23:** nenhuma. As requisições afetadas são HTTP, e a D-23 só mudou no HTTP o tipo do erro interno de autenticação, quando o JWKS está inacessível.

---

## 2. Decisões

| # | Decisão | Motivo |
| --- | --- | --- |
| 1 | **O fim da janela é marcado imediatamente antes do `resume()`** (`resuming := time.Now()`), e uma resposta conta como "durante a queda" só se terminou antes desse instante | Antes do `unpause`, o banco está congelado, e nenhuma requisição que dependa dele pode responder 200. A partir do `unpause`, o 200 é legítimo |
| 2 | **Nada mais muda:** nem o produto, nem a margem do início, nem as demais asserções do R01 | É a menor mudança que remove a causa |

---

## 3. Testes

| Teste | Prova |
| --- | --- |
| R01 `TestPostgresOutage`, com o atraso artificial de 1,5 s depois do `resume()` (temporário) | **Red** antes da correção: as respostas 200 liberadas pelo `unpause` são contadas. **Green** depois dela: o atraso cai fora da janela |
| R01 sem o atraso, `-count=10` | 10 execuções seguidas sem falha |

**Pronto quando:**
1. O red e o green com o atraso foram vistos.
2. `make check` verde.
3. A suíte completa (`make check`, `make test-integration`, `make test-e2e`) verde 3 vezes seguidas, como pedido pelo autor antes dos commits.

---

## 4. Documentos

- [`test-plan.md`](../../test-plan.md), linha do R01: as respostas contam até o início do `resume`.
- O [diário](../diary.md) registra a causa e a correção.

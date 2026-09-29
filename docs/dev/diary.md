# Diário de desenvolvimento

Anotações curtas do autor: o que foi feito em cada sessão e onde o trabalho parou. Os detalhes ficam nas specs, nos planos e nos commits.

---

## 28/09/2026 (seg): documentação e planejamento

- Leitura do `CHALLENGE.md` e redação de toda a documentação de sistema em `docs/`:
  - requisitos de entrega, decisões D-01 a D-20, modelo de dados, ciclo de vida das transações e mensageria;
  - plano de testes, estrutura, stack, fluxo de desenvolvimento e plano de implementação M0–M12.
- `ARCHITECTURE.md` criado como documento vivo, revisado ao fim de cada marco.
- Regras de qualidade definidas: spec → plano → TDD → verificação por marco, com commits só com a autorização do autor.
- Revisão geral de todos os documentos contra o `CHALLENGE.md` e commit inicial em 13 commits atômicos.

## 28–29/09/2026 (seg–ter): M0, esqueleto, qualidade e infraestrutura

**Spikes** (`docs/dev/spike-*.md`):
- **MiniStack 1.5.18:** toda a topologia funciona (SQS FIFO, redrive, visibilidade, SNS FIFO → SQS FIFO com raw delivery). Com `AUTH=true`, as políticas IAM são **avaliadas**. O autor decidiu aplicá-las (D-02): usuários IAM com políticas de identidade e chaves geradas pelo `aws-init` em `.local/aws/credentials`.
- **Keycloak 26.7.4:** `iss` estável com `KC_HOSTNAME` + backchannel dinâmico, e verificação via JWKS direto, sem discovery. O mapper padrão `audience resolve` foi removido do realm (D-07).
- **Lint:** golangci-lint v2.14.0 funciona com Go 1.27.1, e o `forbidigo` captura `float`. O literal inferido (`x := 1.5`) fica para o U01g.

**Esqueleto** ([spec](specs/2026-09-28-m0-skeleton-design.md) → [plano](plans/2026-09-28-m0-skeleton.md) → execução com TDD):
- **Módulos Fx:** `config`, `observability`, `postgres`, `aws` e `httpapi`, com `/health/live`, `/health/ready` (PostgreSQL + SQS) e `/metrics` na porta admin.
- **`pda healthcheck`:** a sonda usada pelo compose, porque a imagem distroless não tem shell nem `curl`.
- **Compose:** postgres, keycloak (realms `pda` e `other`), ministack com `AUTH=true`, `aws-init` idempotente (reaproveita as chaves) e `app-1..3` saudáveis.
- **Testes:**
  - unitários em todos os pacotes;
  - integração: I07a–c e `TestProvisioning`, com checagem de sensibilidade por sabotagem;
  - o `goleak` revelou que os clientes AWS não fechavam conexões no shutdown, e isso foi corrigido.
- **Verificação do zero:** `make check`, `make test-integration` e `docker compose up --build --wait` verdes.
- **Requisitos:** ART-01..04, 06, 07, 10, FX-02, HTTP-08 e OBS-04 marcados; FX-01 e AUTH-09 parciais.
- **Minors adiados:**
  - servidor HTTP que morre depois do start não encerra o processo;
  - réplicas sem `restart:` no compose;
  - logs do Fx em nível INFO.

## 29/09/2026 (ter): M1, domínio com TDD

- [Spec](specs/2026-09-29-m1-domain-design.md) → [plano](plans/2026-09-29-m1-domain.md) → execução com TDD.
- **Abordagem A:** o domínio decide e aplica (`wagering.Settle` e `wagering.OpenWallet`); o `app` do M3 só fará I/O.
- **Pacotes:** `ident`, `money`, `wallet`, `events`, `wagering` e `apperrors`, com a tabela U do test-plan (exceto o U09b, do M2).
- **Decisões novas** (em `decisions.md`): erro não classificado é transitório; chave de idempotência em ASCII visível; IDs do domínio como string canônica; `eventId` atribuído no `Seal`.
- **Achado na validação do plano:** relógio de outra instância atrás do `createdAt` viraria falha permanente; `updatedAt` passou a ter `createdAt` como piso.

## Onde paramos

- **M1 concluído e commitado.** Próximo passo: **M2, persistência** (migrations, repositórios, UoW e testes I01–I03, I16), começando pela spec.
- **Pendências em aberto:**
  - confirmar o horário exato da entrega (assumido 01/10);
  - decidir se os 3 minors do M0 entram em algum marco.
  - **para a spec do M3:** o `app` precisa traduzir explicitamente os erros de invariante do domínio (`money.ErrOverflow`, `wagering.ErrInvalidSnapshot`, `ErrInvalidArgument`, `ErrInvalidTransition`, `wallet.ErrInvalidLedgerEntry`…) para `apperrors.KindPermanent`; pela D-05, um erro não classificado seria tratado como transitório.

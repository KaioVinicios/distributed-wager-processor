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

## Onde paramos

- **M0 concluído e commitado.** Próximo passo: **M1, domínio com TDD** (`money`, `wallet`, `wagering`, `events` e `apperrors`; tabela U do `test-plan.md`), começando pela spec com `superpowers:brainstorming`.
- **Pendências em aberto:**
  - confirmar o horário exato da entrega (assumido 01/10);
  - decidir se os 3 minors do M0 entram em algum marco.

# Stack e Ferramentas

Tecnologias, versões fixadas, ferramentas de qualidade (formatação, lint, análise) e a comprovação de que a stack obrigatória do [`CHALLENGE.md`](../CHALLENGE.md) §4 e §15 é atendida. A organização do código está em [`structure.md`](structure.md), e as justificativas técnicas das escolhas estão em [`decisions.md`](decisions.md).

Versões verificadas no proxy do Go e nos registros de imagens em **28/09/2026**.

---

## 1. Conformidade com a stack obrigatória

### 1.1 Tecnologias obrigatórias (§4)

| Responsabilidade | Exigência | Escolha | Como é comprovado |
| --- | --- | --- | --- |
| Linguagem e compilação | Go, com a versão declarada em `go.mod` e no Dockerfile | **Go 1.27.1**: `go 1.27.1` no `go.mod` e `FROM golang:1.27.1-alpine` no Dockerfile | `make go-version-check` falha se as duas versões divergirem (§5) |
| Dependências | Go Modules, com `go.mod` e `go.sum` versionados | Go Modules; build com `GOFLAGS=-mod=readonly` | `make tidy-check` (`go mod tidy -diff`) e build reproduzível no Docker |
| Composição da aplicação | Uber Fx | `go.uber.org/fx` com `fx.Module`, `fx.Provide`, `fx.Invoke` e `fx.Lifecycle` | [`structure.md`](structure.md) §3; teste I07a–c |
| HTTP | `net/http` ou roteador Go | `net/http.ServeMux` com padrões de método e path | `internal/adapters/httpapi/routes.go` |
| Autenticação | IdP externo OAuth 2.0/OIDC (Keycloak recomendado) | Keycloak 26.7.4 com `client_credentials`; validação com `go-oidc` | Realm importado automaticamente; testes A01–A04 |
| Persistência | PostgreSQL | PostgreSQL 18.6 | `docker-compose.yml`; testes de integração |
| Mensageria | AWS SQS com LocalStack ou MiniStack | SQS (e SNS para saída) no **MiniStack 1.5.18**, via `aws-sdk-go-v2` | `deploy/aws/init.sh`; testes I04, I05 |
| Ambiente local | Docker Compose | `docker-compose.yml` com healthchecks | `docker compose up --build` |
| Evolução do banco | Migrations versionadas, com aplicação e reversão documentadas | `golang-migrate` com arquivos `up` e `down` | Teste I01 (up → down → up); comandos no README |
| Testes | `testing` e `go test`, incluindo `-race` | Somente `testing` da stdlib, sem framework de asserção, mais `goleak` e `kin-openapi` (validação de contrato) | `make test`, `make test-integration`, `make test-e2e`, todos com `-race` |

### 1.2 Acesso ao banco (§4)

| Exigência | Atendimento |
| --- | --- |
| `pgx` com SQL explícito (preferencial) | `pgx/v5` + `pgxpool`, com SQL escrito à mão. Sem `sqlc` e sem ORM |
| Transações, locks e constraints explícitos e verificáveis | `SELECT … FOR UPDATE`, `SET LOCAL lock_timeout`, `SKIP LOCKED` e constraints nomeadas, todos visíveis no código e em [`data-model.md`](data-model.md) |
| Documentar a biblioteca, o mapeamento de `Money` e a delimitação da transação | D-01, D-03 e D-14, consolidados no `ARCHITECTURE.md` |

### 1.3 Entrega (§15)

| Exigência | Atendimento |
| --- | --- |
| `docker compose up --build` | Sobe infraestrutura + migrations + 3 réplicas da aplicação |
| `go test ./...` | Testes unitários, sem Docker (os testes com infraestrutura ficam atrás de build tags) |
| `go test -race ./...` | Idem, com o detector de corrida |
| `go vet ./...` | Sem avisos, também com as tags (`make vet`) |
| Código formatado com `gofmt` | `make fmt-check` executa `gofmt -l .`, que precisa retornar vazio |
| Dependências reproduzíveis | `go.sum` versionado, `make tidy-check` e imagens fixadas por tag |

---

## 2. Versões fixadas

### 2.1 Dependências Go

| Módulo | Versão | Uso |
| --- | --- | --- |
| `go.uber.org/fx` | v1.24.0 | Composição e ciclo de vida |
| `github.com/jackc/pgx/v5` | v5.11.0 | Driver PostgreSQL e pool |
| `github.com/golang-migrate/migrate/v4` | v4.20.1 | Migrations (biblioteca nos testes, com fonte `iofs`) |
| `github.com/coreos/go-oidc/v3` | v3.21.0 | Verificação de JWT, JWKS e issuer |
| `github.com/aws/aws-sdk-go-v2` | v1.47.1 | Núcleo do SDK |
| `github.com/aws/aws-sdk-go-v2/config` | v1.33.6 | Configuração, endpoint e credenciais |
| `github.com/aws/aws-sdk-go-v2/service/sqs` | v1.52.1 | Consumidor e DLQ |
| `github.com/aws/aws-sdk-go-v2/service/sns` | v1.47.2 | Publicação da outbox |
| `github.com/prometheus/client_golang` | v1.24.1 | Métricas |
| `github.com/google/uuid` | v1.6.0 | UUIDv7 |
| `github.com/caarlos0/env/v11` | v11.4.1 | Leitura da configuração a partir do ambiente |
| `go.uber.org/goleak` | v1.3.0 | Detecção de goroutines vazadas (apenas testes) |
| `github.com/getkin/kin-openapi` | v0.149.0 | Validação de requisição e resposta contra `api/openapi.yaml` (apenas testes, D-20) |

**Frontend de documentação** (sem dependência Go): `swagger-ui-dist` **5.33.0**, carregado por CDN (jsDelivr) na página `/docs`, com versão fixada na URL.

Logs (`log/slog`), HTTP (`net/http`), JSON (`encoding/json`), hash (`crypto/sha256`), aleatoriedade (`math/rand/v2`) e testes (`testing`) vêm da biblioteca padrão.

### 2.2 Imagens Docker

| Imagem | Tag | Uso |
| --- | --- | --- |
| `golang` | `1.27.1-alpine` | Estágio de build do Dockerfile |
| `gcr.io/distroless/static-debian12` | `nonroot` | Runtime: binário estático, sem shell, usuário não-root |
| `postgres` | `18.6-alpine` | Banco |
| `quay.io/keycloak/keycloak` | `26.7.4` | IdP |
| `ministackorg/ministack` | `1.5.18` | Emulador de SQS e SNS |
| `amazon/aws-cli` | `2.36.31` | Provisionamento (`aws-init`) |
| `migrate/migrate` | `v4.20.1` | Serviço `migrate` do compose e comandos up/down do README |
| `golangci/golangci-lint` | `v2.14.0` | Lint e formatação sem instalação local |

A fixação é por tag. Se houver tempo no M12, as tags podem ser trocadas por digest (`@sha256:…`) para garantir que a imagem não mude.

### 2.3 Build

- `CGO_ENABLED=0`, `-trimpath` e `-ldflags="-s -w"` produzem um binário estático e reproduzível. `GOFLAGS=-mod=readonly` faz o build falhar se o `go.mod` estiver desatualizado.
- O `go mod download` fica em uma camada separada do Dockerfile, para aproveitar o cache.
- A imagem é compilada **sem build tags**, ou seja, sem pontos de falha ([`structure.md`](structure.md) §5).
- O `-race` exige cgo e um compilador C. Por isso roda nos testes locais e no CI, nunca na imagem de runtime.

---

## 3. Ferramentas de qualidade

| Ferramenta | Versão | Papel | Comando |
| --- | --- | --- | --- |
| `gofmt` | Toolchain Go | **Critério oficial do desafio** para formatação | `make fmt-check` |
| `gofumpt` | Embutido no golangci-lint v2.14.0 | Formatação mais estrita. A saída é sempre compatível com `gofmt` | `make fmt` |
| `goimports` | Embutido no golangci-lint v2.14.0 | Ordena e agrupa imports (stdlib, terceiros, `github.com/KaioVinicios/pda`) | `make fmt` |
| `golangci-lint` | v2.14.0 | Lint agregado (§4) | `make lint` |
| `go vet` | Toolchain Go | Análise estática oficial, com e sem tags | `make vet` |
| `govulncheck` | v1.8.0 (`golang.org/x/vuln`) | Vulnerabilidades conhecidas nas dependências | `make vuln` |
| `go mod tidy -diff` | Toolchain Go | Garante que `go.mod` e `go.sum` estão limpos | `make tidy-check` |
| EditorConfig | — | Indentação, fim de linha e charset para Go, SQL, YAML, shell e Markdown | `.editorconfig` |

**Por que formatar com gofumpt + goimports:** o `gofumpt` é um superconjunto estrito do `gofmt`, então todo arquivo formatado por ele continua passando em `gofmt -l .`. O `goimports` padroniza os grupos de import. Os dois rodam pelo comando `golangci-lint fmt`, sem instalar nada além do golangci-lint.

**Como executar o golangci-lint:** por padrão, o `Makefile` usa a imagem oficial fixada, que não exige instalação local. Quem tiver o binário instalado (ex.: `brew install golangci-lint`, na mesma versão) pode usá-lo com `make lint GOLANGCI_LINT=golangci-lint`. Não usamos `go install` nem a diretiva `tool` do `go.mod` para o golangci-lint, porque a documentação do projeto recomenda binário ou imagem, e assim as dependências dele não se misturam às da aplicação.

**Por que `govulncheck` via `go run` com versão fixada:** `go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...` roda sem instalação global e sem entrar no `go.mod`.

---

## 4. Lint — `.golangci.yml`

### 4.1 Linters e motivos

Além do conjunto `standard` (`errcheck`, `govet`, `ineffassign`, `staticcheck` e `unused`):

| Linter | Por quê | Requisito |
| --- | --- | --- |
| `depguard` | Impõe as regras de camada de [`structure.md`](structure.md) §2: o domínio não importa Fx, HTTP, AWS nem persistência | DOM-07 |
| `forbidigo` | Proíbe `float32`/`float64` e `strconv.ParseFloat`/`FormatFloat` fora de `observability`, e proíbe `fmt.Print*` (use `slog`) | MON-01, E3 |
| `errorlint` | Uso correto de `errors.Is`/`As` e `%w` com erros encapsulados | DOM-04 |
| `errname` | Convenção `ErrX` e `XError` | DOM-04 |
| `exhaustive` | `switch` sobre `Kind`, `Status`, `FailureCode` e `apperrors.Kind` precisam cobrir todos os casos | TX-06, OPS-15 |
| `contextcheck`, `noctx`, `fatcontext`, `containedctx` | O `context` é propagado, requisições HTTP de saída o carregam e ele não é guardado em structs | DOM-06 |
| `bodyclose` | Os corpos de resposta HTTP (JWKS e clientes de teste) são fechados | — |
| `sqlclosecheck` | `Rows` do pgx são fechados | DB-02 |
| `gosec` | Segurança: credenciais, números aleatórios, permissões | AUTH-* |
| `sloglint` | Logs estruturados consistentes, com chaves em `camelCase` e mensagens estáticas | OBS-01 |
| `promlinter` | Nomes de métricas Prometheus válidos, com unidade no sufixo | OBS-03 |
| `musttag` | Structs serializadas em JSON têm tags explícitas, o que protege os contratos | HTTP-*, OUT-* |
| `thelper` | Helpers de teste chamam `t.Helper()` | — |
| `nolintlint` | Todo `//nolint` indica o linter e tem justificativa | — |
| `gocritic` | Diagnósticos e boas práticas gerais | — |
| `misspell`, `usestdlibvars`, `unconvert`, `intrange`, `copyloopvar` | Limpeza e idiomas modernos (Go 1.22+) | — |

**Proteção dupla contra ponto flutuante:** o `forbidigo` avisa já no editor. O teste U01g (`nofloat_test.go`), que analisa a AST do pacote `money`, é a comprovação executável. Backoff e jitter usam aritmética inteira (`time.Duration` e `rand.Int64N`), para que a regra valha em todo o código fora de `observability`. Só a `observability` converte durações para `float64`, porque a API do Prometheus exige.

### 4.2 Configuração

```yaml
version: "2"

run:
  go: "1.27"
  timeout: 5m
  tests: true
  build-tags:
    - integration
    - e2e

linters:
  default: standard
  enable:
    - bodyclose
    - containedctx
    - contextcheck
    - copyloopvar
    - depguard
    - errname
    - errorlint
    - exhaustive
    - fatcontext
    - forbidigo
    - gocritic
    - gosec
    - intrange
    - misspell
    - musttag
    - noctx
    - nolintlint
    - promlinter
    - sloglint
    - sqlclosecheck
    - thelper
    - unconvert
    - usestdlibvars

  settings:
    depguard:
      rules:
        domain:
          files: ["**/internal/domain/**"]
          deny:
            - { pkg: go.uber.org/fx, desc: "domínio independente de Fx (DOM-07)" }
            - { pkg: net/http, desc: "domínio independente de HTTP (DOM-07)" }
            - { pkg: github.com/aws, desc: "domínio independente de SQS/SNS (DOM-07)" }
            - { pkg: github.com/jackc/pgx, desc: "domínio independente de persistência (DOM-07)" }
            - { pkg: database/sql, desc: "domínio independente de persistência (DOM-07)" }
            - { pkg: github.com/KaioVinicios/pda/internal/app, desc: "domínio não conhece a aplicação" }
            - { pkg: github.com/KaioVinicios/pda/internal/adapters, desc: "domínio não conhece adaptadores" }
            - { pkg: github.com/KaioVinicios/pda/internal/apperrors, desc: "domínio usa apenas os próprios erros" }
        app:
          files: ["**/internal/app/**"]
          deny:
            - { pkg: go.uber.org/fx, desc: "Fx só em bootstrap e adaptadores" }
            - { pkg: net/http, desc: "casos de uso não conhecem HTTP" }
            - { pkg: github.com/aws, desc: "casos de uso não conhecem SQS/SNS" }
            - { pkg: github.com/jackc/pgx, desc: "casos de uso dependem de portas, não do driver" }
            - { pkg: github.com/KaioVinicios/pda/internal/adapters, desc: "portas pertencem ao app" }
            - { pkg: github.com/KaioVinicios/pda/internal/auth, desc: "autorização é feita na borda" }
        apperrors:
          files: ["**/internal/apperrors/**", "!$test"]
          deny:
            - { pkg: github.com/KaioVinicios/pda/internal, desc: "apperrors é um pacote folha" }

    forbidigo:
      analyze-types: true
      forbid:
        - pattern: '^float(32|64)$'
          msg: "dinheiro nunca usa ponto flutuante (CHALLENGE §5.1): use money.Money"
        - pattern: '^strconv\.(ParseFloat|FormatFloat)$'
          msg: "dinheiro nunca usa ponto flutuante (CHALLENGE §5.1): use money.Parse"
        - pattern: '^fmt\.Print(f|ln)?$'
          msg: "use log/slog"

    errcheck:
      check-type-assertions: true
    exhaustive:
      default-signifies-exhaustive: false
    gocritic:
      enabled-tags: [diagnostic, performance]
      disabled-checks: [hugeParam]
    gosec:
      excludes: [G104] # já coberto pelo errcheck
    misspell:
      locale: US
    nolintlint:
      require-explanation: true
      require-specific: true
    sloglint:
      no-mixed-args: true
      key-naming-case: camel
      static-msg: true

  exclusions:
    generated: lax
    presets:
      - common-false-positives
      - std-error-handling
    rules:
      - path: internal/observability/
        linters: [forbidigo] # a API do Prometheus exige float64; nunca é dinheiro
      - path: test/load/
        linters: [forbidigo] # percentis de latência no teste de carga opcional
      - path: _test\.go
        linters: [gosec, containedctx]

formatters:
  enable:
    - gofumpt
    - goimports
  settings:
    gofumpt:
      module-path: github.com/KaioVinicios/pda
    goimports:
      local-prefixes:
        - github.com/KaioVinicios/pda
```

**Verificado no spike do M0** ([`dev/spike-lint.md`](dev/spike-lint.md)):
1. ✅ A imagem do golangci-lint v2.14.0 traz Go 1.27.1 e analisa o módulo com `go 1.27.1`. A configuração acima passa em `golangci-lint config verify`.
2. ✅ O padrão `^float(32|64)$`, com `analyze-types: true`, captura `float32`/`float64` em tipos, variáveis, parâmetros, retornos e conversões, além de `strconv.ParseFloat`/`FormatFloat`. A única lacuna é o float inferido de literal (`x := 1.5`), coberto pelo U01g.

`faultinject_on.go` (tag `faultinject`) fica fora do lint, que não habilita essa tag. Ele é coberto por `go vet -tags=faultinject` no `make vet`.

---

## 5. Alvos do `Makefile`

| Alvo | O que faz |
| --- | --- |
| `make up` | `docker compose up --build` |
| `make down` | `docker compose down -v` |
| `make infra-up` | Sobe só a infraestrutura (`postgres keycloak ministack aws-init migrate`) com `--wait` |
| `make migrate-up` / `make migrate-down` | Aplica todas as migrations / reverte a última (`N=` para mais), via imagem `migrate/migrate` |
| `make fmt` | `golangci-lint fmt` (gofumpt + goimports) |
| `make fmt-check` | `gofmt -l .` vazio **e** `golangci-lint fmt --diff` sem diferenças |
| `make lint` | `golangci-lint run` |
| `make vet` | `go vet ./...` e `go vet -tags=integration,e2e,faultinject ./...` |
| `make vuln` | `go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...` |
| `make tidy-check` | `go mod tidy -diff` |
| `make go-version-check` | Compara a versão do Go no `go.mod` com a do Dockerfile |
| `make test` | `go test -race ./...` |
| `make test-integration` | `infra-up` + `go test -tags=integration -race ./...` |
| `make test-e2e` | `infra-up` + `go test -tags=e2e -race -p 1 -timeout 15m ./test/e2e/...` |
| `make check` | `fmt-check lint vet tidy-check go-version-check test`: o portão antes de cada commit |

Trechos de referência:

```make
GOLANGCI_LINT_VERSION ?= v2.14.0
GOLANGCI_LINT ?= docker run --rm -t \
	-v $(CURDIR):/app -w /app \
	-v $(shell go env GOMODCACHE):/go/pkg/mod \
	-v $(HOME)/.cache/golangci-lint:/root/.cache \
	golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint

go-version-check:
	@mod=$$(awk '/^go /{print $$2}' go.mod); \
	img=$$(sed -nE 's/^FROM golang:([0-9.]+)-alpine.*/\1/p' Dockerfile); \
	test "$$mod" = "$$img" || { echo "go.mod ($$mod) != Dockerfile ($$img)"; exit 1; }

fmt-check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	@$(GOLANGCI_LINT) fmt --diff
```

---

## 6. Política de dependências

1. **Biblioteca padrão primeiro.** Uma dependência nova só entra se resolver algo que a stdlib não resolve razoavelmente, e ela precisa ser registrada na tabela §2.1 com o motivo.
2. **Versões fixadas e `go.sum` versionado.** Atualizações são deliberadas e passam por `make check`.
3. **Dependências de teste** (`goleak`, `kin-openapi`) só são importadas em arquivos `_test.go` ou em `test/`.
4. **Ferramentas** (golangci-lint, govulncheck, migrate) ficam fora do `go.mod`: rodam por imagem Docker ou por `go run` com versão fixa.

---

## 7. Alternativas descartadas

| Alternativa | Motivo |
| --- | --- |
| `chi`, `gin`, `echo` | O `ServeMux` já tem padrões de método e path, e 9 rotas não justificam uma dependência (D-01) |
| `sqlc`, GORM, `database/sql` | `pgx` com SQL explícito é o preferencial do desafio e deixa locks e constraints visíveis |
| `shopspring/decimal` | `int64` em centavos é exato, rápido e trivial de persistir em `BIGINT` (D-03) |
| LocalStack | A versão atual exige conta e token, o que impede a reprodução a partir de um checkout limpo (D-02) |
| `testcontainers-go` | Lento com o Keycloak; a infraestrutura do compose com isolamento por banco e filas atende (D-19) |
| `testify` | O desafio pede `testing`, e as asserções ficam em `test/testkit` |
| `viper` | Config só por ambiente; `caarlos0/env` é menor e tipado |
| `zap`, `zerolog` | `log/slog` da stdlib atende logs JSON estruturados |
| OpenTelemetry | Diferencial opcional (M12) |
| `swaggo/swag` (*code-first*) | Anotações espalhadas pelos handlers. Com os contratos já decididos, faz mais sentido o *design-first* (D-20) |
| `oapi-codegen` | Código gerado é exceção ao TDD e acopla os handlers ao gerador. DTOs escritos à mão + validação de contrato nos testes dão a mesma garantia |
| Redoc | Não permite testar as rotas pela página (sem *try it out*) |
| Swagger UI em container próprio | Exige CORS na API e mais um serviço no compose (D-20) |

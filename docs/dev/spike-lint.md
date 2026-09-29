# Spike — golangci-lint v2.14.0 e `forbidigo`

**Data:** 28/09/2026 · **Marco:** M0 · **Código:** descartável (módulo `go 1.27.1` no scratchpad da sessão, com a `.golangci.yml` extraída de [`stack.md`](../stack.md) §4).

**Perguntas** ([`stack.md`](../stack.md) §4.2):
1. A imagem `golangci/golangci-lint:v2.14.0` analisa um módulo com `go 1.27.1`?
2. O padrão `^float(32|64)$` do `forbidigo` captura `float64` usado como tipo?

## Resultado

1. ✅ A imagem traz `go1.27.1 linux/arm64`, o binário foi compilado com `go1.27.0`, e `golangci-lint config verify` aceita a configuração do `stack.md` sem alterações.
2. ✅ Com `analyze-types: true`, o `forbidigo` capturou:
   - `float64`/`float32` em declaração de tipo, variável, parâmetro, retorno e conversão;
   - `strconv.ParseFloat`, `strconv.FormatFloat` e `fmt.Println`.
   - A exclusão de `internal/observability/` funcionou: nenhum aviso lá.

## Observações

- **Um aviso por linha:** por padrão, o golangci-lint mostra só um problema por linha (`uniq-by-line`). Na primeira execução, 5 dos 9 usos proibidos ficaram ocultos atrás de outro aviso na mesma linha. Não é falha de detecção: corrigir um revela o próximo. Com `--uniq-by-line=false --max-same-issues=0`, apareceram todos os 9.
- **Lacuna:** `x := 1.5` (float **inferido** de literal, sem o identificador de tipo) **não** é capturado. Por isso o U01g ([`test-plan.md`](../test-plan.md)) também precisa reprovar literais de ponto flutuante (`ast.BasicLit` com `Kind == token.FLOAT`) no pacote `money`, além dos identificadores.

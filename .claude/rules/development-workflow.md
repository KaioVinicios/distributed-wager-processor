# Fluxo obrigatório: spec → plano → execução com TDD

Vale para **toda tarefa que cria ou altera código** neste repositório, inclusive correções pontuais. O prazo reduz escopo, nunca o processo. Regras completas em [`docs/development-workflow.md`](../../docs/development-workflow.md).

## Dependência

O fluxo usa as skills do plugin **superpowers** (`superpowers@claude-plugins-official`), habilitado em [`.claude/settings.json`](../settings.json). Se ele não estiver disponível na sessão, instale com `/plugin install superpowers@claude-plugins-official` antes de começar; não siga sem ele.

## Etapas

1. **Spec** com `superpowers:brainstorming`: classificar a tarefa (*spike*, *bounded* ou *architectural*), perguntar só o que `docs/` não responde e escrever a spec em `docs/dev/specs/YYYY-MM-DD-<marco-ou-tema>-design.md`, como delta sobre `docs/`. Decisão nova ou alterada vai também para `docs/decisions.md` e para os documentos afetados. **Portão:** o autor aprova a spec escrita.
2. **Plano** com `superpowers:writing-plans`, em `docs/dev/plans/YYYY-MM-DD-<marco-ou-tema>.md`: tarefas pequenas, cada uma com o teste a escrever primeiro, o comando que deve falhar (e por quê), a implementação mínima e o comando que deve passar. **Portão:** o autor aprova o plano.
3. **Execução** com `superpowers:executing-plans`, inline, e `superpowers:test-driven-development` em cada tarefa:
   - nenhum código de produção sem um teste que falhou antes;
   - o red é uma asserção falhando (use stubs), não só um erro de compilação;
   - testes escritos sobre comportamento que já existe passam pela checagem de sensibilidade (sabotar, ver falhar, desfazer) e registram `// Sensitivity: …`.
4. **Verificação** com `superpowers:verification-before-completion`: nenhuma afirmação de "pronto" sem a saída de `make check` e dos testes com tag (`make test-integration`, `make test-e2e`) na mesma mensagem.

**Proporcionalidade:** uma correção pontual tem spec e plano curtos (poucos parágrafos e poucas tarefas), mas escritos e aprovados. Só *spikes* descartáveis e as exceções de [`docs/development-workflow.md`](../../docs/development-workflow.md) §4.4 (compose, Dockerfile, Makefile, YAML e documentação) dispensam o ciclo.

## Convenções

- **Sem commits, branches ou worktrees automáticos.** Os passos de commit das skills são ignorados; no fim da tarefa, proponha commits atômicos (Conventional Commits, skill `git-commit` do projeto) e espere a autorização do autor.
- **Sem trailer de coautoria de IA** (`Co-authored-by`) nas mensagens de commit.
- **Subagentes só com pedido explícito** do autor.
- `docs/` documenta o sistema; `docs/dev/` guarda só notas de desenvolvimento (specs, planos, spikes, diário).
- Todo marco termina com `ARCHITECTURE.md`, `docs/delivery-requirements.md` (citando os testes) e `docs/dev/diary.md` atualizados.

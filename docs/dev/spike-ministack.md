# Spike — MiniStack 1.5.18

**Data:** 28/09/2026 · **Marco:** M0 · **Código:** descartável (scripts `aws --endpoint-url` no scratchpad da sessão, não versionados).

**Pergunta:** o `ministackorg/ministack:1.5.18` cobre a topologia de D-02, D-13 e [`messaging.md`](../messaging.md)? Existe alguma configuração que **avalie** políticas IAM?

**Resposta curta:**
- A topologia completa funciona sem plano B.
- Com `AUTH=true`, o MiniStack **avalia políticas IAM**, mas com duas diferenças em relação à AWS: não verifica a assinatura e não concede acesso só por policy de recurso (§3).

---

## 1. SQS FIFO

| Verificação | Resultado |
| --- | --- |
| `CreateQueue` FIFO com `RedrivePolicy` (`maxReceiveCount`) | ✅ Atributos devolvidos como na AWS |
| Deduplicação por `MessageDeduplicationId` | ✅ O segundo envio devolve o mesmo `MessageId` e não enfileira |
| `ReceiveMessage` com `MessageSystemAttributeNames=All` | ✅ Devolve `ApproximateReceiveCount`, `MessageGroupId`, `MessageDeduplicationId`, `SequenceNumber` e `SentTimestamp` |
| Várias mensagens do mesmo grupo num receive | ✅ Em ordem, como na AWS |
| Grupo bloqueado enquanto há mensagem em voo | ✅ Com qualquer mensagem do grupo em voo, nenhuma outra do grupo é entregue |
| Isolamento entre grupos | ✅ Com `g1` em voo, `g2` é entregue |
| `ChangeMessageVisibility` 0 | ✅ Re-entrega imediata, com `ApproximateReceiveCount` incrementado |
| `ChangeMessageVisibility` N s | ✅ Invisível durante N s e depois volta |
| Redrive para a DLQ ao exceder `maxReceiveCount` | ✅ Movida no receive seguinte. Na DLQ, preserva `MessageGroupId` e `MessageDeduplicationId` |
| Long polling (`WaitTimeSeconds`) | ✅ Retorna assim que a mensagem chega (~2 s num teste com espera de 10 s) |

**Consequência para o M5:** o backoff via `ChangeMessageVisibility` e o redrive automático de [`messaging.md`](../messaging.md) funcionam como especificado. O plano B do risco "redrive diferente" ([`implementation-plan.md`](../implementation-plan.md) §5) não é necessário.

## 2. SNS FIFO → SQS FIFO

| Verificação | Resultado |
| --- | --- |
| `CreateTopic` com `FifoTopic=true` | ✅ Exige o sufixo `.fifo`, como na AWS |
| `Publish` sem `MessageGroupId` | ✅ Rejeitado com `InvalidParameterException` |
| Deduplicação no tópico | ✅ O publish repetido devolve o mesmo `MessageId` e é entregue uma única vez |
| Assinatura SQS FIFO com `RawMessageDelivery=true` | ✅ Corpo cru; `MessageGroupId`, `MessageDeduplicationId` e `MessageAttributes` preservados |
| Assinatura sem raw delivery | ✅ Envelope `Type: Notification` (não usado no projeto) |
| `SequenceNumber` na fila assinante | ⚠️ Vem vazio. O projeto não depende dele |
| Tópico FIFO → fila **standard** | ⚠️ O MiniStack aceita, mas a AWS recusa. Irrelevante, porque a topologia só usa FIFO |

**Consequência:** D-13 fica como está. O plano B (publicar direto na fila SQS) é descartado.

## 3. Políticas IAM (`AUTH=true`)

O código da imagem tem `core/iam_evaluator.py`, com o algoritmo de avaliação da AWS (Deny explícito > Allow > Deny implícito), ativado por `AUTH=true`. Não existe variante `-full` necessária.

| Verificação | Resultado |
| --- | --- |
| Chave raiz (`test`, o valor de `AWS_ACCESS_KEY_ID` do container ou um ID de conta) | Tudo permitido: é o "root" |
| Chave desconhecida | ❌ `UnrecognizedClientException` |
| Usuário IAM com **policy de identidade** (`PutUserPolicy`) | ✅ Avaliada por ação e por ARN de recurso: `ReceiveMessage` permitido na fila certa e negado em outra fila; `SendMessage`, `sns:Publish` e `CreateQueue` negados sem permissão |
| Usuário **só com policy de recurso** (Allow na queue/topic policy) | ❌ **Negado**. Diferente da AWS, onde, dentro da mesma conta, o Allow da policy de recurso basta |
| `Deny` explícito na policy de recurso | ✅ Respeitado, mesmo quando a policy de identidade permite |
| Entrega SNS → SQS | ✅ Avalia a policy da fila: sem uma policy que permita `sns.amazonaws.com` com `aws:SourceArn`, a entrega é negada (log `SNS fanout: queue policy denies delivery`), como na AWS |
| Secret errado com um access key id válido | ⚠️ **Aceito.** A assinatura SigV4 não é verificada. O principal é identificado só pelo access key id |
| Chaves de acesso | Aleatórias (`AKIA` + 16 hex) a cada `CreateAccessKey`. Não há como fixá-las por configuração |

**Consequências para AUTH-09** (decisão do autor, ver o resumo do spike no chat):
- **É possível aplicar as políticas de verdade.** Para isso:
  - os principals viram usuários IAM com policies de **identidade**;
  - a policy de recurso continua em uso para o `Deny` e para o fan-out do SNS;
  - o `aws-init` grava as chaves geradas num arquivo de credenciais num volume compartilhado, que o SDK lê nativamente (`AWS_SHARED_CREDENTIALS_FILE` + `AWS_PROFILE`).
- **Limitação que continua:** sem verificação de assinatura, a autenticação no broker é só pelo access key id. A **autorização** é real; a autenticação do emulador é fraca.
- **Provisionamento de testes:** os testes de integração continuam usando a chave raiz para criar recursos isolados.

## 4. Outras observações

- **Healthcheck:** `GET /_ministack/health` (também em `/_localstack/health`). A imagem já traz um `HEALTHCHECK` em Python contra esse endpoint.
- **Subida:** pronta em ~1 s.
- **Porta:** 4566. A imagem também expõe a 2222, que o projeto não usa.

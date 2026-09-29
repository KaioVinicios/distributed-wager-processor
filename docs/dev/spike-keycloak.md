# Spike — Keycloak 26.7.4

**Data:** 28/09/2026 · **Marco:** M0 · **Código:** descartável (scripts `curl` contra a admin REST e um `realm-pda.json` mínimo no scratchpad da sessão, não versionados).

**Pergunta:** o `quay.io/keycloak/keycloak:26.7.4` emite, via `client_credentials`, o token que D-07 espera (`iss` estável entre host e rede do compose, `aud` = `pda-api`, roles do client `pda-api` e claim `provider_id`)?

**Resposta curta:** sim, com **uma correção obrigatória em D-07**. O mapper padrão `audience resolve` precisa ser removido; sem isso, o `no-audience-client` recebe `aud: pda-api` e o teste de audience inválida perde o sentido.

---

## 1. Token obtido

`curl -d grant_type=client_credentials -d client_id=provider-a -d client_secret=… http://localhost:8080/realms/pda/protocol/openid-connect/token`. O payload decodificado, depois da correção do §2:

```json
{ "iss": "http://localhost:8080/realms/pda", "aud": "pda-api", "azp": "provider-a",
  "provider_id": "provider-a", "resource_access": { "pda-api": { "roles": ["provider"] } },
  "typ": "Bearer", "exp - iat": 300 }
```

- **Cabeçalho:** `alg: RS256` com `kid`. O JWKS publica uma chave `RS256/sig` e uma `RSA-OAEP/enc`; o verificador deve usar só a de assinatura.
- **`aud` com um único valor vem como string, não como array.** O go-oidc aceita os dois formatos.
- **`provider-short-lived`**, com o atributo de client `access.token.lifespan=5`: `exp - iat = 5`. ✅
- **`wallet-service`:** role `wallet-internal`, sem `provider_id`. ✅
- **`no-role-client`:** sem `resource_access.pda-api`. ✅

## 2. Armadilha: `audience resolve` (corrige D-07)

O client scope padrão `roles` traz o mapper `oidc-audience-resolve-mapper`, que coloca em `aud` **todo client do qual o token tem roles**. Como o `no-audience-client` tem a role `provider` de `pda-api`, o token dele saiu com `aud: ["pda-api", "account"]` **sem nenhum audience mapper**.

**Correção validada:** o realm JSON declara o client scope `roles` **só com o mapper `client roles`** e usa `defaultDefaultClientScopes: ["roles"]`.
- A audience passa a vir exclusivamente do `oidc-audience-mapper` explícito de cada client.
- `no-audience-client`: `aud` ausente e role `provider` presente ✅
- Os tokens ficam sem o ruído de `account`, `realm_access` e `scope: email profile`.

## 3. Issuer entre host e rede do compose

| Cenário | Via `localhost:8080` (host) | Via `keycloak:8080` (container na rede) |
| --- | --- | --- |
| Sem `KC_HOSTNAME` | `iss = http://localhost:8080/realms/pda` | `iss = http://keycloak:8080/realms/pda` ❌ diverge |
| `KC_HOSTNAME=http://localhost:8080` + `KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true` | `iss = http://localhost:8080/realms/pda` | `iss = http://localhost:8080/realms/pda` ✅ |

Com a correção, o discovery consultado de dentro da rede devolve `issuer = http://localhost:8080/realms/pda` e `jwks_uri = http://keycloak:8080/realms/pda/protocol/openid-connect/certs` (backchannel). O JWKS responde por `keycloak:8080`.

**Consequência para o M3:**
- O serviço **não** usa `oidc.NewProvider(OIDC_JWKS_URL…)`, porque o discovery interno tem `issuer` diferente da URL consultada e o go-oidc recusaria.
- O serviço usa `oidc.NewRemoteKeySet(ctx, OIDC_JWKS_URL)` + `oidc.NewVerifier(OIDC_ISSUER, keySet, cfg)`, com `SupportedSigningAlgs: [RS256]`. Isso confirma os `OIDC_ISSUER` e `OIDC_JWKS_URL` separados de D-07.

## 4. Provisionamento declarativo (`--import-realm`)

- Um `realm-pda.json` escrito à mão, montado em `/opt/keycloak/data/import`, com `start-dev --import-realm`, reproduziu o token do §1 sem nenhuma chamada à admin REST (`Realm 'pda' imported`).
- **Roles dos service accounts:** entram como `users[]`, com `serviceAccountClientId` e `clientRoles`.
- **Secrets:** declarados no JSON (`"secret": "…"`). O `partial-export` da admin REST mascara os secrets, então não serve como fonte do arquivo versionado.
- **Chaves de assinatura:** geradas a cada import (o `kid` muda a cada recriação do container). Tokens emitidos antes de recriar o Keycloak deixam de valer, o que é aceitável localmente.

## 5. Subida e healthcheck

- **Tempo:** pronto em ~25 s em `start-dev`, na máquina de desenvolvimento.
- **Health:** com `KC_HEALTH_ENABLED=true`, `GET /health/ready` na porta de gerenciamento **9000** devolve `{"status":"UP"}`.
- **A imagem não tem `curl` nem `wget`.** O healthcheck do compose usa `bash` com `/dev/tcp`:
  ```sh
  exec 3<>/dev/tcp/localhost/9000 && printf 'GET /health/ready HTTP/1.0\r\nHost: localhost\r\n\r\n' >&3 && head -1 <&3 | grep -q ' 200 '
  ```
  A primeira parte (conectar, enviar e ler `HTTP/1.0 200 OK`) foi verificada no spike. O `grep` final entra no M0.

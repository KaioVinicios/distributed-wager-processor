#!/usr/bin/env bash
# Uso: scripts/get-token.sh <client-id> [realm]
# Obtém um access token (client_credentials) com o secret do .env.example
# (ou do .env, que tem precedência). Ex.: scripts/get-token.sh provider-a
set -euo pipefail

client="${1:?uso: get-token.sh <client-id> [realm]}"
realm="${2:-pda}"
root="$(cd "$(dirname "$0")/.." && pwd)"
var="$(echo "$client" | tr 'a-z-' 'A-Z_')_SECRET"

secret=""
for f in "$root/.env.example" "$root/.env"; do
  if [ -f "$f" ]; then
    v="$(grep -E "^${var}=" "$f" | tail -1 | cut -d= -f2- || true)"
    if [ -n "$v" ]; then secret="$v"; fi
  fi
done
if [ -z "$secret" ]; then
  echo "get-token: $var não encontrado no .env.example/.env" >&2
  exit 1
fi

curl -fsS -d grant_type=client_credentials -d "client_id=$client" -d "client_secret=$secret" \
  "${KEYCLOAK_URL:-http://localhost:8080}/realms/$realm/protocol/openid-connect/token" \
  | sed -E 's/.*"access_token":"([^"]+)".*/\1/'
echo # a resposta não termina com quebra de linha; o $(…) remove esta

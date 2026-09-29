#!/usr/bin/env bash
# Cria os papéis do banco (data-model.md): pda_owner (migrations, CREATEDB para
# os bancos isolados dos testes) e pda_app (aplicação, sem DDL). Roda uma vez,
# pelo docker-entrypoint-initdb.d, no primeiro start do volume.
set -euo pipefail

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres \
  --set=owner_pw="$PDA_OWNER_PASSWORD" --set=app_pw="$PDA_APP_PASSWORD" <<'SQL'
CREATE ROLE pda_owner LOGIN CREATEDB PASSWORD :'owner_pw';
CREATE ROLE pda_app LOGIN PASSWORD :'app_pw';
ALTER DATABASE pda OWNER TO pda_owner;
SQL

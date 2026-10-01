#!/usr/bin/env bash
# Uso: scripts/load-test.sh (ou make load-test), com o compose de pé.
# Variáveis: RATE (200 req/s), DURATION (60s), WALLETS (1000). Ver docs/load-test.md.
# Roda o k6 do compose (profile load) contra app-1..3 e mede no PostgreSQL o
# atraso exato da outbox na janela gravada pelo k6 (D-21).
set -uo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
out=.local/load
# The directory is kept and only its files are removed: recreating the source
# of a bind mount leaves Docker Desktop with a stale mount, and k6 then fails
# to write its output (M12).
mkdir -p "$out"
rm -f "$out/summary.txt" "$out/summary.json" "$out/window.env"

export RATE="${RATE:-200}" DURATION="${DURATION:-60s}" WALLETS="${WALLETS:-1000}"
LOAD_UID="$(id -u)"
LOAD_GID="$(id -g)"
export LOAD_UID LOAD_GID

docker compose --profile load run --rm k6
k6_status=$?

if ! grep -qs '^LOAD_START=' "$out/window.env"; then
  echo "load-test: o k6 não gravou a janela da carga ($out/window.env)" >&2
  exit $((k6_status != 0 ? k6_status : 1))
fi
# shellcheck source=/dev/null
. "$out/window.env"

read -r events unpublished p50 p95 p99 max < <(
  docker compose exec -T postgres psql -U postgres -d pda -At -F ' ' -c "
    SELECT count(*),
           count(*) FILTER (WHERE published_at IS NULL),
           coalesce(round(percentile_cont(0.50) WITHIN GROUP (ORDER BY lag_ms)::numeric, 1)::text, 'n/a'),
           coalesce(round(percentile_cont(0.95) WITHIN GROUP (ORDER BY lag_ms)::numeric, 1)::text, 'n/a'),
           coalesce(round(percentile_cont(0.99) WITHIN GROUP (ORDER BY lag_ms)::numeric, 1)::text, 'n/a'),
           coalesce(round(max(lag_ms)::numeric, 1)::text, 'n/a')
    FROM (SELECT published_at,
                 (extract(epoch FROM published_at - occurred_at) * 1000)::float8 AS lag_ms
          FROM outbox_events
          WHERE occurred_at BETWEEN '$LOAD_START' AND '$LOAD_END') e"
)

echo "outbox lag    ${events:-0} events · p50 ${p50:-n/a} · p95 ${p95:-n/a} · p99 ${p99:-n/a} · max ${max:-n/a} ms   (SQL, exact)"
if [ -n "${LOAD_LAG_EVENTS:-}" ] && [ "${LOAD_LAG_EVENTS}" != "${events:-0}" ]; then
  echo "              the lag metric counted ${LOAD_LAG_EVENTS} publications"
fi

status=$k6_status
if [ "${events:-0}" = 0 ]; then
  echo "load-test: nenhum evento na janela $LOAD_START – $LOAD_END; a medição falhou" >&2
  status=1
elif [ "${unpublished:-0}" != 0 ]; then
  echo "load-test: $unpublished eventos da janela ainda sem publicar" >&2
  status=1
fi
exit "$status"
